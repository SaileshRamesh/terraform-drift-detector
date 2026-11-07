package awscloud

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ListIDs implementations power UNMANAGED detection. aws_iam_role has none on
// purpose: every account holds many AWS service-linked and SSO roles that
// Terraform never manages, which would drown real findings.

func (h instanceHandler) ListIDs(ctx context.Context, region string) ([]string, error) {
	p := ec2.NewDescribeInstancesPaginator(h.clients.EC2(region), &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("instance-state-name"),
			Values: []string{"pending", "running", "stopping", "stopped"},
		}},
	})
	var ids []string
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range page.Reservations {
			for _, inst := range r.Instances {
				ids = append(ids, aws.ToString(inst.InstanceId))
			}
		}
	}
	return ids, nil
}

// ListIDs skips each VPC's "default" security group, which AWS creates and
// cannot be deleted.
func (h securityGroupHandler) ListIDs(ctx context.Context, region string) ([]string, error) {
	p := ec2.NewDescribeSecurityGroupsPaginator(h.clients.EC2(region), &ec2.DescribeSecurityGroupsInput{})
	var ids []string
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, sg := range page.SecurityGroups {
			if aws.ToString(sg.GroupName) != "default" {
				ids = append(ids, aws.ToString(sg.GroupId))
			}
		}
	}
	return ids, nil
}

// ListIDs skips the region's default VPC.
func (h vpcHandler) ListIDs(ctx context.Context, region string) ([]string, error) {
	p := ec2.NewDescribeVpcsPaginator(h.clients.EC2(region), &ec2.DescribeVpcsInput{})
	var ids []string
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range page.Vpcs {
			if !aws.ToBool(v.IsDefault) {
				ids = append(ids, aws.ToString(v.VpcId))
			}
		}
	}
	return ids, nil
}

func (h s3BucketHandler) ListIDs(ctx context.Context, region string) ([]string, error) {
	p := s3.NewListBucketsPaginator(h.clients.S3(region), &s3.ListBucketsInput{BucketRegion: aws.String(region)})
	var ids []string
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, b := range page.Buckets {
			ids = append(ids, aws.ToString(b.Name))
		}
	}
	return ids, nil
}
