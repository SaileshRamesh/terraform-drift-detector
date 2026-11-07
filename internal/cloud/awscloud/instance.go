package awscloud

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

type instanceHandler struct{ clients Clients }

func (instanceHandler) Provider() string { return provider }
func (instanceHandler) Type() string     { return "aws_instance" }

func (instanceHandler) FromState(a map[string]any) (*model.Resource, error) {
	return &model.Resource{
		ID: str(a, "id"),
		Attributes: map[string]any{
			"ami":                    str(a, "ami"),
			"instance_type":          str(a, "instance_type"),
			"subnet_id":              str(a, "subnet_id"),
			"private_ip":             str(a, "private_ip"),
			"key_name":               str(a, "key_name"),
			"iam_instance_profile":   str(a, "iam_instance_profile"),
			"vpc_security_group_ids": strSet(a, "vpc_security_group_ids"),
			"ebs_optimized":          boolean(a, "ebs_optimized"),
			"monitoring":             boolean(a, "monitoring"),
			"source_dest_check":      boolean(a, "source_dest_check"),
		},
		Tags: stateTags(a),
	}, nil
}

// FetchActual uses an instance-id filter rather than InstanceIds, because
// InstanceIds fails the whole call when any one id no longer exists.
func (h instanceHandler) FetchActual(ctx context.Context, region string, ids []string) (map[string]*model.Resource, error) {
	client := h.clients.EC2(region)
	out := make(map[string]*model.Resource, len(ids))
	for _, batch := range chunk(ids, filterBatchSize) {
		p := ec2.NewDescribeInstancesPaginator(client, &ec2.DescribeInstancesInput{
			Filters: []ec2types.Filter{{Name: aws.String("instance-id"), Values: batch}},
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, err
			}
			for _, r := range page.Reservations {
				for _, inst := range r.Instances {
					if inst.State != nil && (inst.State.Name == ec2types.InstanceStateNameTerminated ||
						inst.State.Name == ec2types.InstanceStateNameShuttingDown) {
						continue
					}
					out[aws.ToString(inst.InstanceId)] = instanceFromAPI(inst)
				}
			}
		}
	}
	return out, nil
}

func instanceFromAPI(i ec2types.Instance) *model.Resource {
	sgs := make([]string, 0, len(i.SecurityGroups))
	for _, g := range i.SecurityGroups {
		sgs = append(sgs, aws.ToString(g.GroupId))
	}
	profile := ""
	if i.IamInstanceProfile != nil {
		// State stores the profile name; the API returns its ARN.
		profile = lastSegment(aws.ToString(i.IamInstanceProfile.Arn), "/")
	}
	monitoring := i.Monitoring != nil && (i.Monitoring.State == ec2types.MonitoringStateEnabled ||
		i.Monitoring.State == ec2types.MonitoringStatePending)
	return &model.Resource{
		ID: aws.ToString(i.InstanceId),
		Attributes: map[string]any{
			"ami":                    aws.ToString(i.ImageId),
			"instance_type":          string(i.InstanceType),
			"subnet_id":              aws.ToString(i.SubnetId),
			"private_ip":             aws.ToString(i.PrivateIpAddress),
			"key_name":               aws.ToString(i.KeyName),
			"iam_instance_profile":   profile,
			"vpc_security_group_ids": sortedSet(sgs),
			"ebs_optimized":          aws.ToBool(i.EbsOptimized),
			"monitoring":             monitoring,
			"source_dest_check":      aws.ToBool(i.SourceDestCheck),
		},
		Tags: ec2Tags(i.Tags),
	}
}
