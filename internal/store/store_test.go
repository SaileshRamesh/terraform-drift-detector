package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/saileshramesh/terraform-drift-detector/internal/config"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ws(name, managedBy string) config.Workspace {
	return config.Workspace{Name: name, ManagedBy: managedBy, Sources: []config.Source{{Path: name + ".tfstate"}}}
}

func TestWorkspaces(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)

	if err := st.UpsertWorkspace(ctx, ws("api-one", config.ManagedByAPI)); err != nil {
		t.Fatal(err)
	}
	if err := st.SyncConfigWorkspaces(ctx, []config.Workspace{ws("prod", ""), ws("stage", "")}); err != nil {
		t.Fatal(err)
	}
	// Re-sync without "stage": it must disappear, the API workspace must stay.
	if err := st.SyncConfigWorkspaces(ctx, []config.Workspace{ws("prod", "")}); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "api-one" || list[1].Name != "prod" || list[1].ManagedBy != config.ManagedByConfig {
		t.Fatalf("unexpected workspaces: %+v", list)
	}

	if _, err := st.GetWorkspace(ctx, "stage"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
	if err := st.DeleteWorkspace(ctx, "api-one"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteWorkspace(ctx, "api-one"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: want ErrNotFound, got %v", err)
	}
}

func TestScans(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	base := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

	for i, wsName := range []string{"prod", "stage", "prod"} {
		r := &model.Report{
			ScanID: "scan-" + string(rune('a'+i)), Workspace: wsName, Status: model.StatusCompleted,
			StartedAt: base.Add(time.Duration(i) * time.Minute), FinishedAt: base.Add(time.Duration(i)*time.Minute + time.Second),
			Summary: model.Summary{Resources: 3, Deleted: i},
			Drifts:  []model.Drift{{Address: "aws_vpc.main", Kind: model.Deleted}},
		}
		if err := st.SaveScan(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	all, err := st.ListScans(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "scan-c" || all[2].ID != "scan-a" {
		t.Fatalf("want newest first, got %+v", all)
	}
	if all[2].HasDrift || !all[0].HasDrift {
		t.Error("HasDrift should follow the summary")
	}
	prod, _ := st.ListScans(ctx, "prod", 10)
	if len(prod) != 2 {
		t.Errorf("prod scans = %d, want 2", len(prod))
	}

	got, err := st.GetScan(ctx, "scan-b")
	if err != nil {
		t.Fatal(err)
	}
	if got.Workspace != "stage" || len(got.Drifts) != 1 {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestFailInterrupted(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	_ = st.SaveScan(ctx, &model.Report{ScanID: "r1", Workspace: "prod", Status: model.StatusRunning, StartedAt: time.Now()})
	_ = st.SaveScan(ctx, &model.Report{ScanID: "c1", Workspace: "prod", Status: model.StatusCompleted, StartedAt: time.Now()})

	n, err := st.FailInterrupted(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	r, _ := st.GetScan(ctx, "r1")
	if r.Status != model.StatusFailed || r.Error == "" {
		t.Errorf("running scan not failed: %+v", r)
	}
}
