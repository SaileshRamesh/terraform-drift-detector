package awscloud

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

type vpcHandler struct{ clients Clients }

func (vpcHandler) Provider() string { return provider }
func (vpcHandler) Type() string     { return "aws_vpc" }

func (vpcHandler) FromState(a map[string]any) (*model.Resource, error) {
	return &model.Resource{
		ID: str(a, "id"),
		Attributes: map[string]any{
			"cidr_block":       str(a, "cidr_block"),
			"instance_tenancy": str(a, "instance_tenancy"),
		},
		Tags: stateTags(a),
	}, nil
}

func (h vpcHandler) FetchActual(ctx context.Context, region string, ids []string) (map[string]*model.Resource, error) {
	client := h.clients.EC2(region)
	out := make(map[string]*model.Resource, len(ids))
	for _, batch := range chunk(ids, filterBatchSize) {
		p := ec2.NewDescribeVpcsPaginator(client, &ec2.DescribeVpcsInput{
			Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: batch}},
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, err
			}
			for _, v := range page.Vpcs {
				id := aws.ToString(v.VpcId)
				out[id] = &model.Resource{
					ID: id,
					Attributes: map[string]any{
						"cidr_block":       aws.ToString(v.CidrBlock),
						"instance_tenancy": string(v.InstanceTenancy),
					},
					Tags: ec2Tags(v.Tags),
				}
			}
		}
	}
	return out, nil
}
