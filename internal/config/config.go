// Package config loads drift.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"

	"github.com/saileshramesh/terraform-drift-detector/internal/engine"
)

// DefaultWorkspaceName is used for the top-level sources in drift.yaml and
// for ad-hoc `tfdrift scan --state ...` runs.
const DefaultWorkspaceName = "default"

const (
	ManagedByConfig = "config"
	ManagedByAPI    = "api"
)

type Config struct {
	// Database is the SQLite file holding workspaces and scan history.
	Database    string `yaml:"database"`
	API         API    `yaml:"api"`
	Concurrency int    `yaml:"concurrency"`

	// Top-level settings form the default workspace, and aws/ignore are
	// inherited by every named workspace.
	Sources         []Source        `yaml:"sources"`
	AWS             AWS             `yaml:"aws"`
	Ignore          []engine.Ignore `yaml:"ignore"`
	DetectUnmanaged bool            `yaml:"detect_unmanaged"`

	Workspaces []Workspace `yaml:"workspaces"`
}

type API struct {
	Addr string `yaml:"addr"`
	// APIKey, when set, is required on /api/ requests as X-API-Key or a
	// Bearer token.
	APIKey string `yaml:"api_key"`
}

type AWS struct {
	Profile string `yaml:"profile" json:"profile,omitempty"`
	Region  string `yaml:"region" json:"region,omitempty"`
}

// Workspace is a named drift-detection target: a set of state files plus the
// cloud settings and schedule used to scan them.
type Workspace struct {
	Name            string          `yaml:"name" json:"name"`
	Sources         []Source        `yaml:"sources" json:"sources"`
	AWS             AWS             `yaml:"aws" json:"aws"`
	Ignore          []engine.Ignore `yaml:"ignore" json:"ignore,omitempty"`
	DetectUnmanaged bool            `yaml:"detect_unmanaged" json:"detect_unmanaged"`
	// Schedule is a standard 5-field cron expression; empty disables it.
	Schedule string `yaml:"schedule" json:"schedule,omitempty"`
	// ManagedBy records whether the workspace comes from drift.yaml or the API.
	ManagedBy string `yaml:"-" json:"managed_by,omitempty"`
}

// Source is one Terraform state file: exactly one of Path, S3 or URL is set.
type Source struct {
	Path string    `yaml:"path,omitempty" json:"path,omitempty"`
	S3   *S3Source `yaml:"s3,omitempty" json:"s3,omitempty"`
	// URL is an http(s) endpoint serving the raw state JSON.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
}

type S3Source struct {
	Bucket string `yaml:"bucket" json:"bucket"`
	Key    string `yaml:"key" json:"key"`
	// Region of the state bucket; defaults to the workspace AWS region.
	Region string `yaml:"region,omitempty" json:"region,omitempty"`
}

// Load reads and validates a config file. Named workspaces inherit the
// top-level aws settings and ignore rules.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	seen := map[string]bool{}
	for i := range c.Workspaces {
		ws := &c.Workspaces[i]
		ws.ManagedBy = ManagedByConfig
		if ws.AWS.Profile == "" {
			ws.AWS.Profile = c.AWS.Profile
		}
		if ws.AWS.Region == "" {
			ws.AWS.Region = c.AWS.Region
		}
		ws.Ignore = append(append([]engine.Ignore{}, c.Ignore...), ws.Ignore...)
		if err := ws.Validate(); err != nil {
			return nil, fmt.Errorf("%s: workspace %q: %w", path, ws.Name, err)
		}
		if seen[ws.Name] || ws.Name == DefaultWorkspaceName {
			return nil, fmt.Errorf("%s: duplicate or reserved workspace name %q", path, ws.Name)
		}
		seen[ws.Name] = true
	}
	for i, s := range c.Sources {
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("%s: sources[%d]: %w", path, i, err)
		}
	}
	return &c, nil
}

// DefaultWorkspace returns the workspace formed by the top-level settings.
func (c *Config) DefaultWorkspace() Workspace {
	return Workspace{
		Name:            DefaultWorkspaceName,
		Sources:         c.Sources,
		AWS:             c.AWS,
		Ignore:          c.Ignore,
		DetectUnmanaged: c.DetectUnmanaged,
		ManagedBy:       ManagedByConfig,
	}
}

// AllWorkspaces returns the named workspaces, plus the default one when
// top-level sources are configured.
func (c *Config) AllWorkspaces() []Workspace {
	all := append([]Workspace{}, c.Workspaces...)
	if len(c.Sources) > 0 {
		all = append(all, c.DefaultWorkspace())
	}
	return all
}

func (c *Config) Workspace(name string) (Workspace, bool) {
	for _, ws := range c.AllWorkspaces() {
		if ws.Name == name {
			return ws, true
		}
	}
	return Workspace{}, false
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func (w Workspace) Validate() error {
	if !namePattern.MatchString(w.Name) {
		return errors.New("name must be lowercase letters, digits, '-' or '_' (max 63)")
	}
	if len(w.Sources) == 0 {
		return errors.New("at least one source is required")
	}
	for i, s := range w.Sources {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("sources[%d]: %w", i, err)
		}
	}
	return ValidateSchedule(w.Schedule)
}

// ValidateSchedule accepts an empty string (no schedule) or a standard cron
// expression, including descriptors such as @hourly.
func ValidateSchedule(spec string) error {
	if spec == "" {
		return nil
	}
	if _, err := cron.ParseStandard(spec); err != nil {
		return fmt.Errorf("invalid schedule %q: %w", spec, err)
	}
	return nil
}

// ParseSource accepts a local path, an s3://bucket/key URL or an http(s) URL.
func ParseSource(s string) (Source, error) {
	if rest, ok := strings.CutPrefix(s, "s3://"); ok {
		bucket, key, _ := strings.Cut(rest, "/")
		if bucket == "" || key == "" {
			return Source{}, fmt.Errorf("invalid S3 state URL %q: want s3://bucket/key", s)
		}
		return Source{S3: &S3Source{Bucket: bucket, Key: key}}, nil
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return Source{URL: s}, nil
	}
	return Source{Path: s}, nil
}

func (s Source) Validate() error {
	set := 0
	for _, ok := range []bool{s.Path != "", s.S3 != nil, s.URL != ""} {
		if ok {
			set++
		}
	}
	switch {
	case set != 1:
		return errors.New("set exactly one of path, s3 or url")
	case s.S3 != nil && (s.S3.Bucket == "" || s.S3.Key == ""):
		return errors.New("s3 source needs bucket and key")
	case s.URL != "" && !strings.HasPrefix(s.URL, "http://") && !strings.HasPrefix(s.URL, "https://"):
		return errors.New("url must start with http:// or https://")
	}
	return nil
}
