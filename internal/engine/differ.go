// Package engine compares the expected (state) and actual (cloud) models.
package engine

import (
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

// Ignore suppresses known-noisy attributes. Type and Attributes are glob
// patterns (path.Match syntax); tags are addressed as "tags.<key>".
type Ignore struct {
	Type       string   `yaml:"type" json:"type"`
	Attributes []string `yaml:"attributes" json:"attributes"`
}

// awsReservedTagPrefix marks tags that AWS services set (CloudFormation, Auto
// Scaling...). Users and Terraform cannot write them, so they are never drift.
const awsReservedTagPrefix = "aws:"

// highRiskTypes get high severity for attribute drift because changes to
// them usually alter the security posture.
var highRiskTypes = map[string]bool{
	"aws_security_group": true,
	"aws_iam_role":       true,
}

type Differ struct {
	ignores []Ignore
}

func NewDiffer(ignores []Ignore) *Differ { return &Differ{ignores: ignores} }

// Compare returns the drift for one resource. actual is nil when the resource
// no longer exists in the cloud. A resource can produce both a MODIFIED and a
// TAGS_CHANGED entry.
func (d *Differ) Compare(expected, actual *model.Resource) []model.Drift {
	base := model.Drift{
		Address: expected.Address, Type: expected.Type, ID: expected.ID, Region: expected.Region,
	}
	if actual == nil {
		base.Kind, base.Severity = model.Deleted, model.High
		base.Message = "resource exists in Terraform state but not in the cloud provider"
		return []model.Drift{base}
	}

	var drifts []model.Drift
	if changes := d.attributeChanges(expected, actual); len(changes) > 0 {
		m := base
		m.Kind, m.Severity, m.Changes = model.Modified, model.Medium, changes
		if highRiskTypes[expected.Type] {
			m.Severity = model.High
		}
		drifts = append(drifts, m)
	}
	if changes := d.tagChanges(expected, actual); len(changes) > 0 {
		t := base
		t.Kind, t.Severity, t.Changes = model.TagsChanged, model.Low, changes
		drifts = append(drifts, t)
	}
	return drifts
}

func (d *Differ) attributeChanges(expected, actual *model.Resource) []model.Change {
	var changes []model.Change
	for _, k := range unionKeys(expected.Attributes, actual.Attributes) {
		if d.ignored(expected.Type, k) {
			continue
		}
		e, a := expected.Attributes[k], actual.Attributes[k]
		if !equal(e, a) {
			changes = append(changes, model.Change{Path: k, Expected: e, Actual: a})
		}
	}
	return changes
}

func (d *Differ) tagChanges(expected, actual *model.Resource) []model.Change {
	var changes []model.Change
	for _, k := range unionKeys(expected.Tags, actual.Tags) {
		p := "tags." + k
		if strings.HasPrefix(k, awsReservedTagPrefix) || d.ignored(expected.Type, p) {
			continue
		}
		e, eok := expected.Tags[k]
		a, aok := actual.Tags[k]
		if e == a && eok == aok {
			continue
		}
		c := model.Change{Path: p}
		if eok {
			c.Expected = e
		}
		if aok {
			c.Actual = a
		}
		changes = append(changes, c)
	}
	return changes
}

func (d *Differ) ignored(tfType, attrPath string) bool {
	for _, ig := range d.ignores {
		typePattern := ig.Type
		if typePattern == "" {
			typePattern = "*"
		}
		if ok, _ := path.Match(typePattern, tfType); !ok {
			continue
		}
		for _, p := range ig.Attributes {
			if ok, _ := path.Match(p, attrPath); ok {
				return true
			}
		}
	}
	return false
}

// equal treats empty and nil string lists as the same value.
func equal(a, b any) bool {
	return reflect.DeepEqual(emptyToNil(a), emptyToNil(b))
}

func emptyToNil(v any) any {
	if s, ok := v.([]string); ok && len(s) == 0 {
		return nil
	}
	return v
}

func unionKeys[V any](a, b map[string]V) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
