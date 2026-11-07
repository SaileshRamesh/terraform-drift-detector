package awscloud

import (
	"context"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/normalize"
)

type iamRoleHandler struct{ clients Clients }

func (iamRoleHandler) Provider() string { return provider }
func (iamRoleHandler) Type() string     { return "aws_iam_role" }

func (iamRoleHandler) FromState(a map[string]any) (*model.Resource, error) {
	return &model.Resource{
		ID:     str(a, "id"),
		Region: globalRegion,
		Attributes: map[string]any{
			"path":                 str(a, "path"),
			"description":          str(a, "description"),
			"max_session_duration": integer(a, "max_session_duration"),
			"permissions_boundary": str(a, "permissions_boundary"),
			"assume_role_policy":   normalize.PolicyJSON(str(a, "assume_role_policy")),
		},
		Tags: stateTags(a),
	}, nil
}

func (h iamRoleHandler) FetchActual(ctx context.Context, _ string, ids []string) (map[string]*model.Resource, error) {
	client := h.clients.IAM()
	return fetchEach(ctx, ids, func(ctx context.Context, name string) (*model.Resource, error) {
		out, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
		if err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		r := out.Role
		// GetRole returns the trust policy URL-encoded.
		policy, err := url.QueryUnescape(aws.ToString(r.AssumeRolePolicyDocument))
		if err != nil {
			policy = aws.ToString(r.AssumeRolePolicyDocument)
		}
		boundary := ""
		if r.PermissionsBoundary != nil {
			boundary = aws.ToString(r.PermissionsBoundary.PermissionsBoundaryArn)
		}
		tags := make(map[string]string, len(r.Tags))
		for _, t := range r.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		return &model.Resource{
			ID: name,
			Attributes: map[string]any{
				"path":                 aws.ToString(r.Path),
				"description":          aws.ToString(r.Description),
				"max_session_duration": int64(aws.ToInt32(r.MaxSessionDuration)),
				"permissions_boundary": boundary,
				"assume_role_policy":   normalize.PolicyJSON(policy),
			},
			Tags: tags,
		}, nil
	})
}
