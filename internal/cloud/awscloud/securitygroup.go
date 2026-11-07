package awscloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

type securityGroupHandler struct{ clients Clients }

func (securityGroupHandler) Provider() string { return provider }
func (securityGroupHandler) Type() string     { return "aws_security_group" }

func (securityGroupHandler) FromState(a map[string]any) (*model.Resource, error) {
	id := str(a, "id")
	return &model.Resource{
		ID: id,
		Attributes: map[string]any{
			"name":        str(a, "name"),
			"description": str(a, "description"),
			"vpc_id":      str(a, "vpc_id"),
			"ingress":     stateRules(a["ingress"], id),
			"egress":      stateRules(a["egress"], id),
		},
		Tags: stateTags(a),
	}, nil
}

func (h securityGroupHandler) FetchActual(ctx context.Context, region string, ids []string) (map[string]*model.Resource, error) {
	client := h.clients.EC2(region)
	out := make(map[string]*model.Resource, len(ids))
	for _, batch := range chunk(ids, filterBatchSize) {
		p := ec2.NewDescribeSecurityGroupsPaginator(client, &ec2.DescribeSecurityGroupsInput{
			Filters: []ec2types.Filter{{Name: aws.String("group-id"), Values: batch}},
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, err
			}
			for _, sg := range page.SecurityGroups {
				id := aws.ToString(sg.GroupId)
				out[id] = &model.Resource{
					ID: id,
					Attributes: map[string]any{
						"name":        aws.ToString(sg.GroupName),
						"description": aws.ToString(sg.Description),
						"vpc_id":      aws.ToString(sg.VpcId),
						"ingress":     apiRules(sg.IpPermissions),
						"egress":      apiRules(sg.IpPermissionsEgress),
					},
					Tags: ec2Tags(sg.Tags),
				}
			}
		}
	}
	return out, nil
}

// Terraform groups rule sources by description, while the API groups them by
// protocol/port. Both sides are flattened into one string per
// (protocol, ports, source, description) so they compare as sorted sets.

func stateRules(raw any, selfID string) []string {
	list, _ := raw.([]any)
	var rules []string
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		add := func(source string) {
			rules = append(rules, ruleString(str(m, "protocol"), integer(m, "from_port"), integer(m, "to_port"), source, str(m, "description")))
		}
		for _, c := range strSet(m, "cidr_blocks") {
			add("cidr:" + c)
		}
		for _, c := range strSet(m, "ipv6_cidr_blocks") {
			add("cidr6:" + c)
		}
		for _, p := range strSet(m, "prefix_list_ids") {
			add("pl:" + p)
		}
		for _, g := range strSet(m, "security_groups") {
			add("sg:" + g)
		}
		if boolean(m, "self") {
			add("sg:" + selfID)
		}
	}
	return sortedSet(rules)
}

func apiRules(perms []ec2types.IpPermission) []string {
	var rules []string
	for _, p := range perms {
		proto := aws.ToString(p.IpProtocol)
		from, to := int64(aws.ToInt32(p.FromPort)), int64(aws.ToInt32(p.ToPort))
		for _, r := range p.IpRanges {
			rules = append(rules, ruleString(proto, from, to, "cidr:"+aws.ToString(r.CidrIp), aws.ToString(r.Description)))
		}
		for _, r := range p.Ipv6Ranges {
			rules = append(rules, ruleString(proto, from, to, "cidr6:"+aws.ToString(r.CidrIpv6), aws.ToString(r.Description)))
		}
		for _, r := range p.PrefixListIds {
			rules = append(rules, ruleString(proto, from, to, "pl:"+aws.ToString(r.PrefixListId), aws.ToString(r.Description)))
		}
		for _, r := range p.UserIdGroupPairs {
			rules = append(rules, ruleString(proto, from, to, "sg:"+aws.ToString(r.GroupId), aws.ToString(r.Description)))
		}
	}
	return sortedSet(rules)
}

func ruleString(proto string, from, to int64, source, desc string) string {
	proto = normalizeProtocol(proto)
	if proto == "all" {
		from, to = 0, 0
	}
	s := fmt.Sprintf("%s %d-%d %s", proto, from, to, source)
	if desc != "" {
		s += fmt.Sprintf(" %q", desc)
	}
	return s
}

func normalizeProtocol(p string) string {
	switch p = strings.ToLower(p); p {
	case "-1", "all":
		return "all"
	case "6":
		return "tcp"
	case "17":
		return "udp"
	case "1":
		return "icmp"
	case "58":
		return "icmpv6"
	}
	return p
}
