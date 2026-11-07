package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saileshramesh/terraform-drift-detector/internal/config"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/runner"
	"github.com/saileshramesh/terraform-drift-detector/internal/scheduler"
	"github.com/saileshramesh/terraform-drift-detector/internal/store"
)

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	runner *runner.Runner
	store  *store.Store
}

func newHarness(t *testing.T, apiKey string) *harness {
	t.Helper()
	// Keep the AWS SDK away from the developer's real credentials.
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rn := runner.New(ctx, st, 2)
	srv := httptest.NewServer(New(st, rn, scheduler.New(st, rn), apiKey).Handler())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		rn.Wait()
		st.Close()
	})
	return &harness{t: t, srv: srv, runner: rn, store: st}
}

func (h *harness) do(method, path, body string, headers ...string) (int, map[string]any) {
	h.t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

const prodJSON = `{"name": "prod", "sources": [{"path": "/nonexistent/prod.tfstate"}], "aws": {"region": "us-east-1"}}`

func TestWorkspaceLifecycle(t *testing.T) {
	h := newHarness(t, "")

	if code, _ := h.do("POST", "/api/v1/workspaces", prodJSON); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if code, _ := h.do("POST", "/api/v1/workspaces", prodJSON); code != http.StatusConflict {
		t.Errorf("duplicate create: %d", code)
	}
	if code, body := h.do("POST", "/api/v1/workspaces", `{"name": "Bad Name", "sources": []}`); code != http.StatusBadRequest {
		t.Errorf("invalid create: %d %v", code, body)
	}

	if code, body := h.do("PUT", "/api/v1/workspaces/prod/schedule", `{"cron": "not cron"}`); code != http.StatusBadRequest {
		t.Errorf("bad cron: %d %v", code, body)
	}
	if code, _ := h.do("PUT", "/api/v1/workspaces/prod/schedule", `{"cron": "0 */6 * * *"}`); code != http.StatusOK {
		t.Errorf("set schedule: %d", code)
	}
	code, body := h.do("GET", "/api/v1/workspaces/prod", "")
	if code != http.StatusOK || body["schedule"] != "0 */6 * * *" || body["next_run"] == nil {
		t.Errorf("get: %d %v", code, body)
	}

	if code, _ := h.do("DELETE", "/api/v1/workspaces/prod", ""); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/workspaces/prod", ""); code != http.StatusNotFound {
		t.Errorf("get after delete: %d", code)
	}
}

func TestConfigWorkspacesAreReadOnly(t *testing.T) {
	h := newHarness(t, "")
	var ws config.Workspace
	_ = json.Unmarshal([]byte(prodJSON), &ws)
	if err := h.store.SyncConfigWorkspaces(context.Background(), []config.Workspace{ws}); err != nil {
		t.Fatal(err)
	}
	for _, req := range [][2]string{{"DELETE", "/api/v1/workspaces/prod"}, {"PUT", "/api/v1/workspaces/prod/schedule"}} {
		if code, _ := h.do(req[0], req[1], `{"cron": "@hourly"}`); code != http.StatusConflict {
			t.Errorf("%s %s: %d, want 409", req[0], req[1], code)
		}
	}
}

func TestTriggerScanRecordsFailure(t *testing.T) {
	h := newHarness(t, "")
	h.do("POST", "/api/v1/workspaces", prodJSON)

	code, body := h.do("POST", "/api/v1/workspaces/prod/scans", "")
	if code != http.StatusAccepted {
		t.Fatalf("trigger: %d %v", code, body)
	}
	id, _ := body["scan_id"].(string)
	h.runner.Wait()

	code, body = h.do("GET", "/api/v1/scans/"+id, "")
	if code != http.StatusOK || body["status"] != model.StatusFailed || !strings.Contains(body["error"].(string), "prod.tfstate") {
		t.Fatalf("scan: %d %v", code, body)
	}

	resp, err := http.Get(h.srv.URL + "/api/v1/scans/" + id + "/report?format=text")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Errorf("text report: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	code, _ = h.do("GET", "/api/v1/scans?workspace=prod", "")
	if code != http.StatusOK {
		t.Errorf("list scans: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/scans/missing", ""); code != http.StatusNotFound {
		t.Errorf("missing scan: %d", code)
	}
}

func TestAPIKey(t *testing.T) {
	h := newHarness(t, "s3cret")
	if code, _ := h.do("GET", "/api/v1/workspaces", ""); code != http.StatusUnauthorized {
		t.Errorf("no key: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/workspaces", "", "X-API-Key", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong key: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/workspaces", "", "Authorization", "Bearer s3cret"); code != http.StatusOK {
		t.Errorf("bearer key: %d", code)
	}
	// Health and dashboard stay public.
	for _, path := range []string{"/health", "/", "/app.js"} {
		resp, err := http.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
}
