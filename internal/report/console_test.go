package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

func TestWriteConsole(t *testing.T) {
	rep := &model.Report{
		ScanID:  "test",
		Sources: []string{"terraform.tfstate"},
		Summary: model.Summary{Resources: 4, InSync: 1, Deleted: 1, Modified: 1, TagsChanged: 1, Skipped: 1},
		Drifts: []model.Drift{
			{Address: "aws_instance.app[0]", Type: "aws_instance", ID: "i-1", Region: "us-east-1", Kind: model.Deleted, Severity: model.High, Message: "gone"},
			{Address: "aws_security_group.web", Type: "aws_security_group", ID: "sg-1", Kind: model.Modified, Severity: model.High, Changes: []model.Change{
				{Path: "ingress", Expected: []string{"tcp 443-443 cidr:10.0.0.0/8"}, Actual: []string{"tcp 22-22 cidr:0.0.0.0/0", "tcp 443-443 cidr:10.0.0.0/8"}},
				{Path: "description", Expected: "web", Actual: "web tier"},
			}},
			{Address: "aws_vpc.main", Type: "aws_vpc", ID: "vpc-1", Kind: model.TagsChanged, Severity: model.Low, Changes: []model.Change{
				{Path: "tags.Owner", Expected: "alice", Actual: nil},
			}},
			{Address: "aws_lambda_function.worker", Type: "aws_lambda_function", Kind: model.Skipped, Severity: model.Info},
		},
	}
	var buf bytes.Buffer
	if err := WriteConsole(&buf, rep, ConsoleOptions{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	t.Log("\n" + out)
	for _, want := range []string{
		"+ tcp 22-22 cidr:0.0.0.0/0",
		`description: "web" → "web tier"`,
		`tags.Owner: "alice" → (absent)`,
		"aws_lambda_function ×1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if strings.Contains(out, "- tcp 443-443") {
		t.Error("unchanged list elements must not be shown")
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("colour codes emitted with Color disabled")
	}
}
