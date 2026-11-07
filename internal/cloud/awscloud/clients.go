// Package awscloud implements the AWS cloud fetcher and resource handlers.
package awscloud

import (
	"context"
	"errors"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
)

// The narrow API interfaces below are what handlers depend on, so tests can
// substitute fakes without touching the network.

type EC2API interface {
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
}

type S3API interface {
	HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
	GetBucketTagging(ctx context.Context, in *s3.GetBucketTaggingInput, opts ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListBuckets(ctx context.Context, in *s3.ListBucketsInput, opts ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
}

type IAMAPI interface {
	GetRole(ctx context.Context, in *iam.GetRoleInput, opts ...func(*iam.Options)) (*iam.GetRoleOutput, error)
}

// Clients hands out per-region API clients.
type Clients interface {
	EC2(region string) EC2API
	S3(region string) S3API
	IAM() IAMAPI
}

// Handlers returns every AWS resource handler, bound to clients.
func Handlers(c Clients) []cloud.Handler {
	return []cloud.Handler{
		instanceHandler{c},
		securityGroupHandler{c},
		vpcHandler{c},
		subnetHandler{c},
		s3BucketHandler{c},
		iamRoleHandler{c},
	}
}

// SDKClients builds real AWS SDK clients, cached per region.
type SDKClients struct {
	cfg aws.Config

	mu  sync.Mutex
	ec2 map[string]*ec2.Client
	s3  map[string]*s3.Client
	iam *iam.Client
}

// NewSDKClients loads credentials through the standard AWS chain (env vars,
// shared config/profile, SSO, instance/task roles). Adaptive retries absorb
// API throttling during large scans.
func NewSDKClients(ctx context.Context, profile, region string) (*SDKClients, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRetryMode(aws.RetryModeAdaptive),
		config.WithRetryMaxAttempts(10),
	}
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		return nil, errors.New("no AWS region configured: set aws.region in drift.yaml, --region, or AWS_REGION")
	}
	return &SDKClients{cfg: cfg, ec2: map[string]*ec2.Client{}, s3: map[string]*s3.Client{}}, nil
}

func (c *SDKClients) DefaultRegion() string { return c.cfg.Region }

// resolve maps the pseudo-region "global" (IAM etc.) and "" to the default.
func (c *SDKClients) resolve(region string) string {
	if region == "" || region == globalRegion {
		return c.cfg.Region
	}
	return region
}

func (c *SDKClients) EC2(region string) EC2API {
	region = c.resolve(region)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.ec2[region]; ok {
		return cl
	}
	cl := ec2.NewFromConfig(c.cfg, func(o *ec2.Options) { o.Region = region })
	c.ec2[region] = cl
	return cl
}

func (c *SDKClients) S3(region string) S3API {
	region = c.resolve(region)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cl, ok := c.s3[region]; ok {
		return cl
	}
	cl := s3.NewFromConfig(c.cfg, func(o *s3.Options) { o.Region = region })
	c.s3[region] = cl
	return cl
}

func (c *SDKClients) IAM() IAMAPI {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.iam == nil {
		c.iam = iam.NewFromConfig(c.cfg)
	}
	return c.iam
}
