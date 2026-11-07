package scan

import (
	"context"
	"errors"
	"testing"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/engine"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/state"
)

type bytesReader string

func (b bytesReader) Source() string                       { return "test.tfstate" }
func (b bytesReader) Read(context.Context) ([]byte, error) { return []byte(b), nil }

// fakeHandler reports "size" from state and from a canned cloud inventory.
type fakeHandler struct {
	tfType string
	cloud  map[string]string
	err    error
}

func (h fakeHandler) Provider() string { return "fake" }
func (h fakeHandler) Type() string     { return h.tfType }

func (h fakeHandler) FromState(a map[string]any) (*model.Resource, error) {
	id, _ := a["id"].(string)
	size, _ := a["size"].(string)
	return &model.Resource{ID: id, Attributes: map[string]any{"size": size}, Tags: map[string]string{}}, nil
}

func (h fakeHandler) FetchActual(_ context.Context, _ string, ids []string) (map[string]*model.Resource, error) {
	if h.err != nil {
		return nil, h.err
	}
	out := map[string]*model.Resource{}
	for _, id := range ids {
		if size, ok := h.cloud[id]; ok {
			out[id] = &model.Resource{ID: id, Attributes: map[string]any{"size": size}, Tags: map[string]string{}}
		}
	}
	return out, nil
}

const tfstate = `{"version": 4, "resources": [
  {"mode": "managed", "type": "fake_disk", "name": "a", "instances": [
    {"index_key": 0, "attributes": {"id": "d-1", "size": "10"}},
    {"index_key": 1, "attributes": {"id": "d-2", "size": "10"}},
    {"index_key": 2, "attributes": {"id": "d-3", "size": "10"}}
  ]},
  {"mode": "managed", "type": "fake_broken", "name": "b", "instances": [{"attributes": {"id": "b-1"}}]},
  {"mode": "managed", "type": "fake_unknown", "name": "c", "instances": [{"attributes": {"id": "u-1"}}]}
]}`

func TestRun(t *testing.T) {
	reg := cloud.NewRegistry(
		fakeHandler{tfType: "fake_disk", cloud: map[string]string{"d-1": "10", "d-2": "20"}},
		fakeHandler{tfType: "fake_broken", err: errors.New("AccessDenied")},
	)
	rep, err := Run(context.Background(), Options{
		Readers:     []state.Reader{bytesReader(tfstate)},
		Registry:    reg,
		Differ:      engine.NewDiffer(nil),
		Concurrency: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := model.Summary{Resources: 5, InSync: 1, Deleted: 1, Modified: 1, Errors: 1, Skipped: 1}
	if rep.Summary != want {
		t.Errorf("summary = %+v, want %+v", rep.Summary, want)
	}
	if !rep.HasDrift() {
		t.Error("HasDrift should be true")
	}
	order := []model.DriftKind{model.Deleted, model.Modified, model.Error, model.Skipped}
	for i, d := range rep.Drifts {
		if d.Kind != order[i] {
			t.Errorf("drift %d kind %s, want %s", i, d.Kind, order[i])
		}
	}
	if rep.Drifts[0].Address != "fake_disk.a[2]" {
		t.Errorf("deleted resource = %s", rep.Drifts[0].Address)
	}
}

// listingHandler adds unmanaged-detection support to fakeHandler.
type listingHandler struct {
	fakeHandler
	listed []string
}

func (h listingHandler) ListIDs(context.Context, string) ([]string, error) { return h.listed, nil }

func TestRunDetectsUnmanaged(t *testing.T) {
	disks := listingHandler{
		fakeHandler: fakeHandler{tfType: "fake_disk", cloud: map[string]string{"d-1": "10", "d-2": "10", "d-3": "10"}},
		listed:      []string{"d-1", "d-2", "d-3", "d-orphan"},
	}
	opts := Options{
		Readers:  []state.Reader{bytesReader(tfstate)},
		Registry: cloud.NewRegistry(disks),
		Differ:   engine.NewDiffer(nil),
	}

	rep, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Unmanaged != 0 {
		t.Fatal("unmanaged detection must be opt-in")
	}

	opts.DetectUnmanaged = true
	rep, err = Run(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Unmanaged != 1 || rep.Summary.InSync != 3 || !rep.HasDrift() {
		t.Fatalf("summary = %+v", rep.Summary)
	}
	var found bool
	for _, d := range rep.Drifts {
		if d.Kind == model.Unmanaged {
			found = d.ID == "d-orphan" && d.Type == "fake_disk" && d.Address == ""
		}
	}
	if !found {
		t.Fatalf("d-orphan not reported as unmanaged: %+v", rep.Drifts)
	}
}

func TestRunFailedState(t *testing.T) {
	rep, err := Run(context.Background(), Options{
		ScanID:   "fixed-id",
		Readers:  []state.Reader{bytesReader(`{"version": 3}`)},
		Registry: cloud.NewRegistry(),
		Differ:   engine.NewDiffer(nil),
	})
	if err == nil || rep == nil || rep.Status != model.StatusFailed || rep.Error == "" || rep.ScanID != "fixed-id" {
		t.Fatalf("expected failed report, got %+v, %v", rep, err)
	}
}
