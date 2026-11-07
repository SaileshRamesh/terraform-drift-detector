package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "drift.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadWorkspacesInheritDefaults(t *testing.T) {
	c, err := load(t, `
aws: {region: us-east-1, profile: ops}
ignore: [{attributes: ["tags.Owner"]}]
sources: [{path: root.tfstate}]
workspaces:
  - name: prod
    aws: {region: eu-west-1}
    schedule: "0 */6 * * *"
    ignore: [{type: aws_instance, attributes: [ami]}]
    sources:
      - s3: {bucket: state, key: prod.tfstate}
      - url: https://example.com/state
`)
	if err != nil {
		t.Fatal(err)
	}
	prod, ok := c.Workspace("prod")
	if !ok {
		t.Fatal("prod missing")
	}
	if prod.AWS.Region != "eu-west-1" || prod.AWS.Profile != "ops" {
		t.Errorf("aws not inherited: %+v", prod.AWS)
	}
	if len(prod.Ignore) != 2 || prod.ManagedBy != ManagedByConfig {
		t.Errorf("ignore not merged: %+v", prod.Ignore)
	}
	if len(c.AllWorkspaces()) != 2 {
		t.Errorf("want prod + default, got %d", len(c.AllWorkspaces()))
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	for name, yaml := range map[string]string{
		"bad cron":       "workspaces: [{name: a, schedule: 'every day', sources: [{path: x}]}]",
		"bad name":       "workspaces: [{name: Prod!, sources: [{path: x}]}]",
		"reserved name":  "workspaces: [{name: default, sources: [{path: x}]}]",
		"duplicate":      "workspaces: [{name: a, sources: [{path: x}]}, {name: a, sources: [{path: y}]}]",
		"two sources":    "sources: [{path: x, url: 'https://e'}]",
		"no sources":     "workspaces: [{name: a}]",
		"unknown field":  "colour: blue",
		"bad url scheme": "sources: [{url: 'ftp://e/x'}]",
	} {
		if _, err := load(t, yaml); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestParseSource(t *testing.T) {
	cases := map[string]func(Source) bool{
		"s3://b/k/terraform.tfstate": func(s Source) bool { return s.S3 != nil && s.S3.Key == "k/terraform.tfstate" },
		"https://host/state":         func(s Source) bool { return s.URL == "https://host/state" },
		"./terraform.tfstate":        func(s Source) bool { return s.Path == "./terraform.tfstate" },
	}
	for in, ok := range cases {
		s, err := ParseSource(in)
		if err != nil || !ok(s) {
			t.Errorf("ParseSource(%q) = %+v, %v", in, s, err)
		}
	}
	if _, err := ParseSource("s3://bucket-only"); err == nil || !strings.Contains(err.Error(), "s3://bucket/key") {
		t.Errorf("want s3 format error, got %v", err)
	}
}
