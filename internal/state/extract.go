package state

import (
	"errors"
	"strings"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

// Extract builds the expected resource model from a parsed state file.
// Instances whose type has no registered handler, or that cannot be
// normalized, are returned as SKIPPED/ERROR drift entries so coverage gaps
// stay visible in the report.
func Extract(f *File, reg *cloud.Registry, defaultRegion string) ([]*model.Resource, []model.Drift) {
	var resources []*model.Resource
	var problems []model.Drift
	for _, r := range f.Resources {
		if r.Mode != "managed" {
			continue
		}
		h, ok := reg.Get(r.Type)
		for _, inst := range r.Instances {
			addr := r.Address(inst)
			if !ok {
				problems = append(problems, model.Drift{
					Address: addr, Type: r.Type, Kind: model.Skipped, Severity: model.Info,
					Message: "no handler registered for this resource type",
				})
				continue
			}
			res, err := h.FromState(inst.Attributes)
			if err == nil && res.ID == "" {
				err = errors.New("resource has no id in state")
			}
			if err != nil {
				problems = append(problems, model.Drift{
					Address: addr, Type: r.Type, Kind: model.Error, Severity: model.Medium,
					Message: "reading state: " + err.Error(),
				})
				continue
			}
			res.Provider = h.Provider()
			res.Type = r.Type
			res.Address = addr
			if res.Region == "" {
				res.Region = regionOf(inst.Attributes, defaultRegion)
			}
			resources = append(resources, res)
		}
	}
	return resources, problems
}

// regionOf prefers an explicit region attribute, then the region embedded in
// the resource ARN, then the configured default.
func regionOf(attrs map[string]any, defaultRegion string) string {
	if r, _ := attrs["region"].(string); r != "" {
		return r
	}
	if arn, _ := attrs["arn"].(string); arn != "" {
		if parts := strings.SplitN(arn, ":", 5); len(parts) == 5 && parts[3] != "" {
			return parts[3]
		}
	}
	return defaultRegion
}
