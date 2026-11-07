// Package state reads Terraform state files (the left-hand pipeline) and
// extracts the expected resource model from them.
package state

import (
	"encoding/json"
	"fmt"
)

// File is the subset of the Terraform state v4 format the scanner needs.
type File struct {
	Version          int        `json:"version"`
	TerraformVersion string     `json:"terraform_version"`
	Serial           int64      `json:"serial"`
	Lineage          string     `json:"lineage"`
	Resources        []Resource `json:"resources"`
}

type Resource struct {
	Module    string     `json:"module,omitempty"`
	Mode      string     `json:"mode"`
	Type      string     `json:"type"`
	Name      string     `json:"name"`
	Provider  string     `json:"provider"`
	Instances []Instance `json:"instances"`
}

type Instance struct {
	IndexKey   any            `json:"index_key,omitempty"`
	Attributes map[string]any `json:"attributes"`
}

func Parse(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("invalid state JSON: %w", err)
	}
	if f.Version != 4 {
		return nil, fmt.Errorf("unsupported state format version %d (expected 4)", f.Version)
	}
	return &f, nil
}

// Address returns the Terraform address of one instance, e.g.
// module.web.aws_instance.app[0] or aws_s3_bucket.logs["eu"].
func (r Resource) Address(inst Instance) string {
	addr := r.Type + "." + r.Name
	if r.Module != "" {
		addr = r.Module + "." + addr
	}
	switch k := inst.IndexKey.(type) {
	case float64:
		addr += fmt.Sprintf("[%d]", int(k))
	case string:
		addr += fmt.Sprintf("[%q]", k)
	}
	return addr
}
