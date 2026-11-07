package awscloud

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

type subnetHandler struct{ clients Clients }

func (subnetHandler) Provider() string { return provider }
func (subnetHandler) Type() string     { return "aws_subnet" }

func (subnetHandler) FromState(a map[string]any) (*model.Resource, error) {
	return &model.Resource{
		ID: str(a, "id"),
		Attributes: map[string]any{
			"vpc_id":                          str(a, "vpc_id"),
			"cidr_block":                      str(a, "cidr_block"),
			"ipv6_cidr_block":                 str(a, "ipv6_cidr_block"),
			"availability_zone":               str(a, "availability_zone"),
			"map_public_ip_on_launch":         boolean(a, "map_public_ip_on_launch"),
			"assign_ipv6_address_on_creation": boolean(a, "assign_ipv6_address_on_creation"),
		},
		Tags: stateTags(a),
	}, nil
}

func (h subnetHandler) FetchActual(ctx context.Context, region string, ids []string) (map[string]*model.Resource, error) {
	out := make(map[string]*model.Resource, len(ids))
	for _, batch := range chunk(ids, filterBatchSize) {
		err := h.describe(ctx, region, []ec2types.Filter{{Name: aws.String("subnet-id"), Values: batch}}, func(s ec2types.Subnet) {
			ipv6 := ""
			for _, assoc := range s.Ipv6CidrBlockAssociationSet {
				if assoc.Ipv6CidrBlockState != nil && assoc.Ipv6CidrBlockState.State == ec2types.SubnetCidrBlockStateCodeAssociated {
					ipv6 = aws.ToString(assoc.Ipv6CidrBlock)
				}
			}
			id := aws.ToString(s.SubnetId)
			out[id] = &model.Resource{
				ID: id,
				Attributes: map[string]any{
					"vpc_id":                          aws.ToString(s.VpcId),
					"cidr_block":                      aws.ToString(s.CidrBlock),
					"ipv6_cidr_block":                 ipv6,
					"availability_zone":               aws.ToString(s.AvailabilityZone),
					"map_public_ip_on_launch":         aws.ToBool(s.MapPublicIpOnLaunch),
					"assign_ipv6_address_on_creation": aws.ToBool(s.AssignIpv6AddressOnCreation),
				},
				Tags: ec2Tags(s.Tags),
			}
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListIDs skips the default subnets AWS creates in each availability zone.
func (h subnetHandler) ListIDs(ctx context.Context, region string) ([]string, error) {
	var ids []string
	err := h.describe(ctx, region, nil, func(s ec2types.Subnet) {
		if !aws.ToBool(s.DefaultForAz) {
			ids = append(ids, aws.ToString(s.SubnetId))
		}
	})
	return ids, err
}

func (h subnetHandler) describe(ctx context.Context, region string, filters []ec2types.Filter, fn func(ec2types.Subnet)) error {
	p := ec2.NewDescribeSubnetsPaginator(h.clients.EC2(region), &ec2.DescribeSubnetsInput{Filters: filters})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, s := range page.Subnets {
			fn(s)
		}
	}
	return nil
}
