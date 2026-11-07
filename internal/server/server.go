// Package server exposes the REST API and the web dashboard.
package server

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/saileshramesh/terraform-drift-detector/internal/config"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/report"
	"github.com/saileshramesh/terraform-drift-detector/internal/runner"
	"github.com/saileshramesh/terraform-drift-detector/internal/scheduler"
	"github.com/saileshramesh/terraform-drift-detector/internal/store"
)

//go:embed web
var webFiles embed.FS

const maxBodyBytes = 1 << 20

type Server struct {
	store  *store.Store
	runner *runner.Runner
	sched  *scheduler.Scheduler
	apiKey string
	mux    *http.ServeMux
}

func New(st *store.Store, rn *runner.Runner, sched *scheduler.Scheduler, apiKey string) *Server {
	s := &Server{store: st, runner: rn, sched: sched, apiKey: apiKey, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.auth(s.mux) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	s.mux.HandleFunc("GET /api/v1/workspaces", s.listWorkspaces)
	s.mux.HandleFunc("POST /api/v1/workspaces", s.createWorkspace)
	s.mux.HandleFunc("GET /api/v1/workspaces/{name}", s.getWorkspace)
	s.mux.HandleFunc("PUT /api/v1/workspaces/{name}", s.putWorkspace)
	s.mux.HandleFunc("DELETE /api/v1/workspaces/{name}", s.deleteWorkspace)
	s.mux.HandleFunc("POST /api/v1/workspaces/{name}/scans", s.triggerScan)
	s.mux.HandleFunc("GET /api/v1/workspaces/{name}/scans", s.listWorkspaceScans)
	s.mux.HandleFunc("PUT /api/v1/workspaces/{name}/schedule", s.putSchedule)
	s.mux.HandleFunc("DELETE /api/v1/workspaces/{name}/schedule", s.deleteSchedule)
	s.mux.HandleFunc("GET /api/v1/scans", s.listScans)
	s.mux.HandleFunc("GET /api/v1/scans/{id}", s.getScan)
	s.mux.HandleFunc("GET /api/v1/scans/{id}/report", s.getReport)

	static, _ := fs.Sub(webFiles, "web")
	s.mux.Handle("GET /", http.FileServerFS(static))
}

// auth protects /api/ when an API key is configured. The dashboard and
// /health stay public; the dashboard asks the user for the key.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			key := r.Header.Get("X-API-Key")
			if key == "" {
				key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			}
			if subtle.ConstantTimeCompare([]byte(key), []byte(s.apiKey)) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or invalid API key")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// workspaceView adds runtime status to a workspace.
type workspaceView struct {
	config.Workspace
	NextRun   *time.Time        `json:"next_run,omitempty"`
	RunningID string            `json:"running_scan_id,omitempty"`
	LastScan  *store.ScanRecord `json:"last_scan,omitempty"`
}

func (s *Server) view(ctx context.Context, ws config.Workspace) (workspaceView, error) {
	v := workspaceView{Workspace: ws}
	if next, ok := s.sched.Next(ws.Name); ok {
		v.NextRun = &next
	}
	v.RunningID, _ = s.runner.Running(ws.Name)
	scans, err := s.store.ListScans(ctx, ws.Name, 1)
	if err != nil {
		return v, err
	}
	if len(scans) > 0 {
		v.LastScan = &scans[0]
	}
	return v, nil
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	wss, err := s.store.ListWorkspaces(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]workspaceView, 0, len(wss))
	for _, ws := range wss {
		v, err := s.view(r.Context(), ws)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.lookup(w, r)
	if !ok {
		return
	}
	v, err := s.view(r.Context(), *ws)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var ws config.Workspace
	if !decode(w, r, &ws) {
		return
	}
	if _, err := s.store.GetWorkspace(r.Context(), ws.Name); err == nil {
		writeError(w, http.StatusConflict, "workspace already exists; use PUT to replace it")
		return
	}
	s.saveWorkspace(w, r, ws, http.StatusCreated)
}

func (s *Server) putWorkspace(w http.ResponseWriter, r *http.Request) {
	var ws config.Workspace
	if !decode(w, r, &ws) {
		return
	}
	if ws.Name == "" {
		ws.Name = r.PathValue("name")
	}
	if ws.Name != r.PathValue("name") {
		writeError(w, http.StatusBadRequest, "name in body does not match the URL")
		return
	}
	if existing, err := s.store.GetWorkspace(r.Context(), ws.Name); err == nil && !s.editable(w, existing) {
		return
	}
	s.saveWorkspace(w, r, ws, http.StatusOK)
}

func (s *Server) saveWorkspace(w http.ResponseWriter, r *http.Request, ws config.Workspace, status int) {
	ws.ManagedBy = config.ManagedByAPI
	if err := ws.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpsertWorkspace(r.Context(), ws); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.sched.Sync(r.Context())
	writeJSON(w, status, ws)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.lookup(w, r)
	if !ok || !s.editable(w, ws) {
		return
	}
	if err := s.store.DeleteWorkspace(r.Context(), ws.Name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.sched.Sync(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) triggerScan(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.lookup(w, r)
	if !ok {
		return
	}
	id, err := s.runner.Start(*ws)
	if errors.Is(err, runner.ErrAlreadyRunning) {
		running, _ := s.runner.Running(ws.Name)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "scan_id": running})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Location", "/api/v1/scans/"+id)
	writeJSON(w, http.StatusAccepted, map[string]string{"scan_id": id, "status": model.StatusRunning})
}

func (s *Server) putSchedule(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.lookup(w, r)
	if !ok || !s.editable(w, ws) {
		return
	}
	var body struct {
		Cron string `json:"cron"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Cron == "" {
		writeError(w, http.StatusBadRequest, "cron is required; use DELETE to remove the schedule")
		return
	}
	if err := config.ValidateSchedule(body.Cron); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ws.Schedule = body.Cron
	s.saveWorkspace(w, r, *ws, http.StatusOK)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.lookup(w, r)
	if !ok || !s.editable(w, ws) {
		return
	}
	ws.Schedule = ""
	s.saveWorkspace(w, r, *ws, http.StatusOK)
}

func (s *Server) listScans(w http.ResponseWriter, r *http.Request) {
	s.writeScans(w, r, r.URL.Query().Get("workspace"))
}

func (s *Server) listWorkspaceScans(w http.ResponseWriter, r *http.Request) {
	s.writeScans(w, r, r.PathValue("name"))
}

func (s *Server) writeScans(w http.ResponseWriter, r *http.Request, workspace string) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	scans, err := s.store.ListScans(r.Context(), workspace, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, scans)
}

func (s *Server) getScan(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.lookupScan(w, r)
	if ok {
		writeJSON(w, http.StatusOK, rep)
	}
}

// getReport renders a scan as JSON (default) or plain console text.
func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
	rep, ok := s.lookupScan(w, r)
	if !ok {
		return
	}
	switch r.URL.Query().Get("format") {
	case "", "json":
		w.Header().Set("Content-Type", "application/json")
		_ = report.WriteJSON(w, rep)
	case "text", "console":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_ = report.WriteConsole(w, rep, report.ConsoleOptions{})
	default:
		writeError(w, http.StatusBadRequest, "format must be json or text")
	}
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (*config.Workspace, bool) {
	ws, err := s.store.GetWorkspace(r.Context(), r.PathValue("name"))
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	return ws, true
}

func (s *Server) lookupScan(w http.ResponseWriter, r *http.Request) (*model.Report, bool) {
	rep, err := s.store.GetScan(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return nil, false
	}
	return rep, true
}

// editable rejects changes to workspaces owned by the config file, which
// would be overwritten on the next restart.
func (s *Server) editable(w http.ResponseWriter, ws *config.Workspace) bool {
	if ws.ManagedBy == config.ManagedByConfig {
		writeError(w, http.StatusConflict, "workspace is defined in the config file; edit it there")
		return false
	}
	return true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusInternalServerError, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
