package awscloud

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

// s3BucketHandler checks existence and tags. Since AWS provider v4, bucket
// settings (versioning, encryption, policy...) live in separate resources
// such as aws_s3_bucket_versioning, which get their own handlers.
type s3BucketHandler struct{ clients Clients }

func (s3BucketHandler) Provider() string { return provider }
func (s3BucketHandler) Type() string     { return "aws_s3_bucket" }

func (s3BucketHandler) FromState(a map[string]any) (*model.Resource, error) {
	return &model.Resource{
		ID:         str(a, "id"),
		Region:     str(a, "region"),
		Attributes: map[string]any{},
		Tags:       stateTags(a),
	}, nil
}

func (h s3BucketHandler) FetchActual(ctx context.Context, region string, ids []string) (map[string]*model.Resource, error) {
	client := h.clients.S3(region)
	return fetchEach(ctx, ids, func(ctx context.Context, bucket string) (*model.Resource, error) {
		if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}); err != nil {
			if isNotFound(err) {
				return nil, nil
			}
			return nil, err
		}
		tags := map[string]string{}
		out, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: aws.String(bucket)})
		switch {
		case err == nil:
			for _, t := range out.TagSet {
				tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
			}
		case errorCode(err) == "NoSuchTagSet":
		default:
			return nil, err
		}
		return &model.Resource{ID: bucket, Attributes: map[string]any{}, Tags: tags}, nil
	})
}
