package awscloud

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// fakeClients serves canned API responses.
type fakeClients struct {
	ec2 *fakeEC2
	iam *fakeIAM
}

func (f fakeClients) EC2(string) EC2API { return f.ec2 }
func (f fakeClients) S3(string) S3API   { return nil }
func (f fakeClients) IAM() IAMAPI       { return f.iam }

type fakeEC2 struct {
	instances []ec2types.Instance
	groups    []ec2types.SecurityGroup
	vpcs      []ec2types.Vpc
	subnets   []ec2types.Subnet
	filters   [][]string
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	if len(in.Filters) > 0 {
		f.filters = append(f.filters, in.Filters[0].Values)
	}
	return &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: f.instances}}}, nil
}

func (f *fakeEC2) DescribeSecurityGroups(context.Context, *ec2.DescribeSecurityGroupsInput, ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: f.groups}, nil
}

func (f *fakeEC2) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
}

func (f *fakeEC2) DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	return &ec2.DescribeSubnetsOutput{Subnets: f.subnets}, nil
}

type fakeIAM struct{ roles map[string]iamtypes.Role }

func (f *fakeIAM) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	r, ok := f.roles[*in.RoleName]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("not found")}
	}
	return &iam.GetRoleOutput{Role: &r}, nil
}

var _ S3API = (*s3.Client)(nil)

// stateAttrs mimics JSON-decoded state attributes.
func stateAttrs(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstanceRoundTrip(t *testing.T) {
	fake := &fakeEC2{instances: []ec2types.Instance{
		{
			InstanceId:         aws.String("i-1"),
			ImageId:            aws.String("ami-1"),
			InstanceType:       ec2types.InstanceTypeT3Micro,
			SubnetId:           aws.String("subnet-1"),
			PrivateIpAddress:   aws.String("10.0.0.5"),
			IamInstanceProfile: &ec2types.IamInstanceProfile{Arn: aws.String("arn:aws:iam::1:instance-profile/team/app")},
			SecurityGroups:     []ec2types.GroupIdentifier{{GroupId: aws.String("sg-b")}, {GroupId: aws.String("sg-a")}},
			Monitoring:         &ec2types.Monitoring{State: ec2types.MonitoringStateDisabled},
			SourceDestCheck:    aws.Bool(true),
			EbsOptimized:       aws.Bool(false),
			State:              &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning},
			Tags:               []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("app")}},
		},
		{InstanceId: aws.String("i-2"), State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameTerminated}},
	}}
	h := instanceHandler{fakeClients{ec2: fake}}

	expected, _ := h.FromState(stateAttrs(t, `{
		"id": "i-1", "ami": "ami-1", "instance_type": "t3.micro", "subnet_id": "subnet-1",
		"private_ip": "10.0.0.5", "iam_instance_profile": "app",
		"vpc_security_group_ids": ["sg-a", "sg-b"], "monitoring": false,
		"source_dest_check": true, "ebs_optimized": false,
		"tags": {"Name": "app"}
	}`))

	actual, err := h.FetchActual(context.Background(), "us-east-1", []string{"i-1", "i-2"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := actual["i-2"]; ok {
		t.Error("terminated instance must be treated as deleted")
	}
	got := actual["i-1"]
	if got == nil {
		t.Fatal("i-1 missing")
	}
	if !reflect.DeepEqual(expected.Attributes, got.Attributes) {
		t.Errorf("attributes differ:\nstate: %#v\ncloud: %#v", expected.Attributes, got.Attributes)
	}
	if !reflect.DeepEqual(expected.Tags, got.Tags) {
		t.Errorf("tags differ: %v vs %v", expected.Tags, got.Tags)
	}
}

func TestInstanceFetchBatches(t *testing.T) {
	fake := &fakeEC2{}
	ids := make([]string, filterBatchSize+1)
	for i := range ids {
		ids[i] = "i-x"
	}
	if _, err := (instanceHandler{fakeClients{ec2: fake}}).FetchActual(context.Background(), "r", ids); err != nil {
		t.Fatal(err)
	}
	if len(fake.filters) != 2 || len(fake.filters[0]) != filterBatchSize || len(fake.filters[1]) != 1 {
		t.Errorf("unexpected batching: %d calls", len(fake.filters))
	}
}

func TestSecurityGroupRulesMatchAcrossShapes(t *testing.T) {
	// Terraform groups by description; the API groups by protocol/ports.
	expected := stateRules(stateAttrs(t, `{"ingress": [
		{"protocol": "tcp", "from_port": 443, "to_port": 443, "cidr_blocks": ["0.0.0.0/0", "10.0.0.0/8"], "description": "https"},
		{"protocol": "-1", "from_port": 0, "to_port": 0, "self": true, "description": ""}
	]}`)["ingress"], "sg-self")

	actual := apiRules([]ec2types.IpPermission{
		{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443), IpRanges: []ec2types.IpRange{
			{CidrIp: aws.String("10.0.0.0/8"), Description: aws.String("https")},
			{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("https")},
		}},
		{IpProtocol: aws.String("-1"), UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-self")}}},
	})

	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("rules differ:\nstate: %q\ncloud: %q", expected, actual)
	}
}

func TestIAMRole(t *testing.T) {
	h := iamRoleHandler{fakeClients{iam: &fakeIAM{roles: map[string]iamtypes.Role{
		"app": {
			RoleName: aws.String("app"),
			Path:     aws.String("/"),
			// URL-encoded and reformatted, as GetRole returns it.
			AssumeRolePolicyDocument: aws.String("%7B%22Statement%22%3A%7B%22Action%22%3A%22sts%3AAssumeRole%22%2C%22Effect%22%3A%22Allow%22%2C%22Principal%22%3A%7B%22Service%22%3A%22ec2.amazonaws.com%22%7D%7D%2C%22Version%22%3A%222012-10-17%22%7D"),
			MaxSessionDuration:       aws.Int32(3600),
		},
	}}}}

	expected, _ := h.FromState(stateAttrs(t, `{
		"id": "app", "path": "/", "max_session_duration": 3600,
		"assume_role_policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"Service\":\"ec2.amazonaws.com\"},\"Action\":\"sts:AssumeRole\"}]}"
	}`))

	actual, err := h.FetchActual(context.Background(), "", []string{"app", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := actual["gone"]; ok {
		t.Error("missing role must be omitted")
	}
	if !reflect.DeepEqual(expected.Attributes, actual["app"].Attributes) {
		t.Errorf("attributes differ:\nstate: %#v\ncloud: %#v", expected.Attributes, actual["app"].Attributes)
	}
}

func TestSubnetRoundTrip(t *testing.T) {
	fake := &fakeEC2{subnets: []ec2types.Subnet{{
		SubnetId:            aws.String("subnet-1"),
		VpcId:               aws.String("vpc-1"),
		CidrBlock:           aws.String("10.0.1.0/24"),
		AvailabilityZone:    aws.String("us-east-1a"),
		MapPublicIpOnLaunch: aws.Bool(true),
		Ipv6CidrBlockAssociationSet: []ec2types.SubnetIpv6CidrBlockAssociation{{
			Ipv6CidrBlock:      aws.String("2600::/64"),
			Ipv6CidrBlockState: &ec2types.SubnetCidrBlockState{State: ec2types.SubnetCidrBlockStateCodeAssociated},
		}},
		Tags: []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("public-a")}},
	}}}
	h := subnetHandler{fakeClients{ec2: fake}}
	expected, _ := h.FromState(stateAttrs(t, `{
		"id": "subnet-1", "vpc_id": "vpc-1", "cidr_block": "10.0.1.0/24", "ipv6_cidr_block": "2600::/64",
		"availability_zone": "us-east-1a", "map_public_ip_on_launch": true,
		"assign_ipv6_address_on_creation": false, "tags_all": {"Name": "public-a"}
	}`))
	actual, err := h.FetchActual(context.Background(), "us-east-1", []string{"subnet-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := actual["subnet-1"]; got == nil || !reflect.DeepEqual(expected.Attributes, got.Attributes) || !reflect.DeepEqual(expected.Tags, got.Tags) {
		t.Fatalf("subnet differs:\nstate: %#v\ncloud: %#v", expected, got)
	}
}

func TestListersSkipAWSDefaults(t *testing.T) {
	fake := &fakeEC2{
		groups: []ec2types.SecurityGroup{
			{GroupId: aws.String("sg-default"), GroupName: aws.String("default")},
			{GroupId: aws.String("sg-web"), GroupName: aws.String("web")},
		},
		vpcs: []ec2types.Vpc{
			{VpcId: aws.String("vpc-default"), IsDefault: aws.Bool(true)},
			{VpcId: aws.String("vpc-main"), IsDefault: aws.Bool(false)},
		},
		subnets: []ec2types.Subnet{
			{SubnetId: aws.String("subnet-default"), DefaultForAz: aws.Bool(true)},
			{SubnetId: aws.String("subnet-app"), DefaultForAz: aws.Bool(false)},
		},
	}
	c := fakeClients{ec2: fake}
	ctx := context.Background()
	for name, tc := range map[string]struct {
		list func() ([]string, error)
		want []string
	}{
		"security groups": {func() ([]string, error) { return securityGroupHandler{c}.ListIDs(ctx, "r") }, []string{"sg-web"}},
		"vpcs":            {func() ([]string, error) { return vpcHandler{c}.ListIDs(ctx, "r") }, []string{"vpc-main"}},
		"subnets":         {func() ([]string, error) { return subnetHandler{c}.ListIDs(ctx, "r") }, []string{"subnet-app"}},
	} {
		got, err := tc.list()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}
}
