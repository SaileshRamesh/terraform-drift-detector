// Package model defines the provider-neutral data model that both the
// Terraform-state pipeline and the cloud pipeline normalize into.
package model

import "time"

// SchemaVersion is bumped whenever the JSON report shape changes incompatibly.
const SchemaVersion = "1"

// Resource is a single resource instance in normalized form. Attributes hold
// only the curated, comparable subset defined by the resource's handler.
type Resource struct {
	Provider   string            `json:"provider"`
	Type       string            `json:"type"`
	Address    string            `json:"address"`
	ID         string            `json:"id"`
	Region     string            `json:"region,omitempty"`
	Attributes map[string]any    `json:"attributes"`
	Tags       map[string]string `json:"tags"`
}

// Key joins expected and actual resources.
func Key(tfType, id string) string { return tfType + "|" + id }

// Key joins expected and actual resources.
func (r *Resource) Key() string { return Key(r.Type, r.ID) }

type DriftKind string

const (
	Deleted     DriftKind = "DELETED"
	Modified    DriftKind = "MODIFIED"
	TagsChanged DriftKind = "TAGS_CHANGED"
	// Unmanaged resources exist in the cloud but in none of the scanned states.
	Unmanaged DriftKind = "UNMANAGED"
	Error     DriftKind = "ERROR"
	Skipped   DriftKind = "SKIPPED"
)

const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

type Severity string

const (
	High   Severity = "high"
	Medium Severity = "medium"
	Low    Severity = "low"
	Info   Severity = "info"
)

// Change is a single attribute difference. Expected comes from Terraform
// state, Actual from the cloud provider. A nil value means "absent".
type Change struct {
	Path     string `json:"path"`
	Expected any    `json:"expected"`
	Actual   any    `json:"actual"`
}

type Drift struct {
	Address  string    `json:"address"`
	Type     string    `json:"type"`
	ID       string    `json:"id,omitempty"`
	Region   string    `json:"region,omitempty"`
	Kind     DriftKind `json:"kind"`
	Severity Severity  `json:"severity"`
	Changes  []Change  `json:"changes,omitempty"`
	Message  string    `json:"message,omitempty"`
}

type Summary struct {
	Resources   int `json:"resources"`
	InSync      int `json:"in_sync"`
	Deleted     int `json:"deleted"`
	Modified    int `json:"modified"`
	TagsChanged int `json:"tags_changed"`
	Unmanaged   int `json:"unmanaged"`
	Errors      int `json:"errors"`
	Skipped     int `json:"skipped"`
}

type Report struct {
	SchemaVersion string    `json:"schema_version"`
	ScanID        string    `json:"scan_id"`
	Workspace     string    `json:"workspace,omitempty"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitzero"`
	DurationMs    int64     `json:"duration_ms"`
	Sources       []string  `json:"sources"`
	Summary       Summary   `json:"summary"`
	Drifts        []Drift   `json:"drifts"`
}

// HasDrift reports whether any resource differs from its Terraform state.
// Skipped resources and fetch errors are not counted as drift.
func (r *Report) HasDrift() bool {
	s := r.Summary
	return s.Deleted+s.Modified+s.TagsChanged+s.Unmanaged > 0
}
