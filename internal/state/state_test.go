package state

import (
	"os"
	"testing"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/cloud/awscloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

func loadFixture(t *testing.T) *File {
	t.Helper()
	data, err := os.ReadFile("../../testdata/terraform.tfstate")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseRejectsOldFormat(t *testing.T) {
	if _, err := Parse([]byte(`{"version":3}`)); err == nil {
		t.Fatal("expected error for state version 3")
	}
}

func TestExtract(t *testing.T) {
	reg := cloud.NewRegistry(awscloud.Handlers(nil)...)
	resources, problems := Extract(loadFixture(t), reg, "us-west-2")

	byAddr := map[string]*model.Resource{}
	for _, r := range resources {
		byAddr[r.Address] = r
	}
	want := map[string]string{ // address -> region
		"aws_vpc.main":                      "us-east-1",
		"module.web.aws_security_group.web": "us-east-1",
		"aws_instance.app[0]":               "us-east-1",
		"aws_instance.app[1]":               "us-east-1",
		`aws_s3_bucket.logs["eu"]`:          "eu-west-1",
		"aws_iam_role.app":                  "global",
	}
	if len(resources) != len(want) {
		t.Fatalf("got %d resources, want %d", len(resources), len(want))
	}
	for addr, region := range want {
		r, ok := byAddr[addr]
		if !ok {
			t.Errorf("missing %s", addr)
			continue
		}
		if r.Region != region {
			t.Errorf("%s: region %q, want %q", addr, r.Region, region)
		}
		if r.Provider != "aws" || r.ID == "" {
			t.Errorf("%s: incomplete resource %+v", addr, r)
		}
	}

	if len(problems) != 1 || problems[0].Kind != model.Skipped || problems[0].Address != "aws_lambda_function.worker" {
		t.Errorf("unexpected problems: %+v", problems)
	}
}
