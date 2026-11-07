package awscloud

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

const (
	provider = "aws"
	// globalRegion marks resources (IAM) that are not regional.
	globalRegion = "global"
	// filterBatchSize is the number of ids sent in one Describe* filter.
	filterBatchSize = 200
	// perResourceConcurrency bounds parallel calls for APIs without batch lookup.
	perResourceConcurrency = 8
)

// Helpers for reading Terraform state attributes (JSON-decoded values).

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func boolean(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

func integer(m map[string]any, k string) int64 {
	f, _ := m[k].(float64)
	return int64(f)
}

func strSet(m map[string]any, k string) []string {
	list, _ := m[k].([]any)
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return sortedSet(out)
}

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

// stateTags prefers tags_all, which includes provider default_tags and so
// matches what the cloud API reports.
func stateTags(a map[string]any) map[string]string {
	if v, ok := a["tags_all"]; ok && v != nil {
		return strMap(v)
	}
	return strMap(a["tags"])
}

// Helpers for API responses.

func sortedSet(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func ec2Tags(tags []ec2types.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		if t.Key != nil {
			out[*t.Key] = aws.ToString(t.Value)
		}
	}
	return out
}

func lastSegment(s, sep string) string { return s[strings.LastIndex(s, sep)+1:] }

func chunk(ids []string, size int) [][]string {
	var out [][]string
	for size < len(ids) {
		ids, out = ids[size:], append(out, ids[:size])
	}
	return append(out, ids)
}

var notFoundCodes = map[string]bool{
	"NotFound": true, "NoSuchBucket": true, "NoSuchEntity": true,
}

func isNotFound(err error) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && notFoundCodes[ae.ErrorCode()]
}

func errorCode(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// fetchEach looks up ids one at a time with bounded parallelism, for APIs
// that have no batch describe call. fn returns nil for ids that do not exist.
func fetchEach(ctx context.Context, ids []string, fn func(ctx context.Context, id string) (*model.Resource, error)) (map[string]*model.Resource, error) {
	var mu sync.Mutex
	out := make(map[string]*model.Resource, len(ids))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(perResourceConcurrency)
	for _, id := range ids {
		g.Go(func() error {
			res, err := fn(ctx, id)
			if err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
			if res != nil {
				mu.Lock()
				out[id] = res
				mu.Unlock()
			}
			return nil
		})
	}
	return out, g.Wait()
}
