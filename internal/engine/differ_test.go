package engine

import (
	"testing"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

func resource(attrs map[string]any, tags map[string]string) *model.Resource {
	return &model.Resource{Type: "aws_instance", Address: "aws_instance.a", ID: "i-1", Attributes: attrs, Tags: tags}
}

func TestCompare(t *testing.T) {
	expected := resource(
		map[string]any{"instance_type": "t3.micro", "sgs": []string{"sg-1"}, "empty": []string{}},
		map[string]string{"Env": "prod", "Owner": "alice", "LastSeen": "x"},
	)

	t.Run("deleted", func(t *testing.T) {
		d := NewDiffer(nil).Compare(expected, nil)
		if len(d) != 1 || d[0].Kind != model.Deleted || d[0].Severity != model.High {
			t.Fatalf("got %+v", d)
		}
	})

	t.Run("in sync", func(t *testing.T) {
		actual := resource(
			map[string]any{"instance_type": "t3.micro", "sgs": []string{"sg-1"}, "empty": []string(nil)},
			map[string]string{"Env": "prod", "Owner": "alice", "LastSeen": "x"},
		)
		if d := NewDiffer(nil).Compare(expected, actual); len(d) != 0 {
			t.Fatalf("expected no drift, got %+v", d)
		}
	})

	t.Run("modified and tags", func(t *testing.T) {
		actual := resource(
			map[string]any{"instance_type": "t3.large", "sgs": []string{"sg-1"}, "empty": []string{}},
			map[string]string{"Env": "dev", "Team": "core", "LastSeen": "y"},
		)
		d := NewDiffer([]Ignore{{Attributes: []string{"tags.Last*"}}}).Compare(expected, actual)
		if len(d) != 2 {
			t.Fatalf("got %d drifts: %+v", len(d), d)
		}
		mod, tags := d[0], d[1]
		if mod.Kind != model.Modified || len(mod.Changes) != 1 || mod.Changes[0].Path != "instance_type" {
			t.Errorf("modified: %+v", mod)
		}
		if tags.Kind != model.TagsChanged {
			t.Fatalf("tags: %+v", tags)
		}
		got := map[string]model.Change{}
		for _, c := range tags.Changes {
			got[c.Path] = c
		}
		if len(got) != 3 {
			t.Errorf("want Env, Owner, Team changes (LastSeen ignored), got %+v", tags.Changes)
		}
		if c := got["tags.Owner"]; c.Expected != "alice" || c.Actual != nil {
			t.Errorf("removed tag: %+v", c)
		}
		if c := got["tags.Team"]; c.Expected != nil || c.Actual != "core" {
			t.Errorf("added tag: %+v", c)
		}
	})

	t.Run("ignore scoped by type", func(t *testing.T) {
		actual := resource(map[string]any{"instance_type": "t3.large", "sgs": []string{"sg-1"}}, expected.Tags)
		d := NewDiffer([]Ignore{{Type: "aws_s3_*", Attributes: []string{"instance_type"}}}).Compare(expected, actual)
		if len(d) != 1 {
			t.Fatalf("ignore for another type must not apply: %+v", d)
		}
	})
}
