// Package store persists workspaces and scan history in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: keeps the binary cgo-free

	"github.com/saileshramesh/terraform-drift-detector/internal/config"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

var ErrNotFound = errors.New("not found")

// timeFormat is fixed-width so stored timestamps sort lexically.
const timeFormat = "2006-01-02T15:04:05.000000Z"

const schema = `
CREATE TABLE IF NOT EXISTS workspaces (
    name       TEXT PRIMARY KEY,
    spec       TEXT NOT NULL,
    managed_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS scans (
    id          TEXT PRIMARY KEY,
    workspace   TEXT NOT NULL,
    status      TEXT NOT NULL,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    duration_ms INTEGER NOT NULL DEFAULT 0,
    summary     TEXT NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    report      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS scans_by_workspace ON scans (workspace, started_at DESC);
CREATE INDEX IF NOT EXISTS scans_by_time ON scans (started_at DESC);
`

// ScanRecord is the list view of a scan, without its drift entries.
type ScanRecord struct {
	ID         string        `json:"scan_id"`
	Workspace  string        `json:"workspace"`
	Status     string        `json:"status"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at,omitzero"`
	DurationMs int64         `json:"duration_ms"`
	Summary    model.Summary `json:"summary"`
	HasDrift   bool          `json:"has_drift"`
	Error      string        `json:"error,omitempty"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer; a single connection avoids SQLITE_BUSY between
	// the API, the scheduler and background scans.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initializing database %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) UpsertWorkspace(ctx context.Context, ws config.Workspace) error {
	spec, err := json.Marshal(ws)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(timeFormat)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO workspaces (name, spec, managed_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET spec = excluded.spec, managed_by = excluded.managed_by, updated_at = excluded.updated_at`,
		ws.Name, string(spec), ws.ManagedBy, now, now)
	return err
}

// SyncConfigWorkspaces makes the config-managed workspaces in the database
// match wss exactly; API-managed workspaces are left alone.
func (s *Store) SyncConfigWorkspaces(ctx context.Context, wss []config.Workspace) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspaces WHERE managed_by = ?`, config.ManagedByConfig); err != nil {
		return err
	}
	now := time.Now().UTC().Format(timeFormat)
	for _, ws := range wss {
		ws.ManagedBy = config.ManagedByConfig
		spec, err := json.Marshal(ws)
		if err != nil {
			return err
		}
		// A config workspace replaces an API one of the same name.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workspaces (name, spec, managed_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (name) DO UPDATE SET spec = excluded.spec, managed_by = excluded.managed_by, updated_at = excluded.updated_at`,
			ws.Name, string(spec), ws.ManagedBy, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) GetWorkspace(ctx context.Context, name string) (*config.Workspace, error) {
	var spec string
	err := s.db.QueryRowContext(ctx, `SELECT spec FROM workspaces WHERE name = ?`, name).Scan(&spec)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("workspace %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var ws config.Workspace
	if err := json.Unmarshal([]byte(spec), &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]config.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []config.Workspace{}
	for rows.Next() {
		var spec string
		if err := rows.Scan(&spec); err != nil {
			return nil, err
		}
		var ws config.Workspace
		if err := json.Unmarshal([]byte(spec), &ws); err != nil {
			return nil, err
		}
		list = append(list, ws)
	}
	return list, rows.Err()
}

// DeleteWorkspace removes the workspace; its scan history is kept.
func (s *Store) DeleteWorkspace(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("workspace %q: %w", name, ErrNotFound)
	}
	return nil
}

func (s *Store) SaveScan(ctx context.Context, r *model.Report) error {
	full, err := json.Marshal(r)
	if err != nil {
		return err
	}
	summary, err := json.Marshal(r.Summary)
	if err != nil {
		return err
	}
	finished := ""
	if !r.FinishedAt.IsZero() {
		finished = r.FinishedAt.UTC().Format(timeFormat)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO scans (id, workspace, status, started_at, finished_at, duration_ms, summary, error, report)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			status = excluded.status, started_at = excluded.started_at, finished_at = excluded.finished_at,
			duration_ms = excluded.duration_ms, summary = excluded.summary, error = excluded.error, report = excluded.report`,
		r.ScanID, r.Workspace, r.Status, r.StartedAt.UTC().Format(timeFormat), finished,
		r.DurationMs, string(summary), r.Error, string(full))
	return err
}

func (s *Store) GetScan(ctx context.Context, id string) (*model.Report, error) {
	var full string
	err := s.db.QueryRowContext(ctx, `SELECT report FROM scans WHERE id = ?`, id).Scan(&full)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("scan %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	var r model.Report
	if err := json.Unmarshal([]byte(full), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListScans returns the newest scans first; workspace "" means all.
func (s *Store) ListScans(ctx context.Context, workspace string, limit int) ([]ScanRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	query := `SELECT id, workspace, status, started_at, finished_at, duration_ms, summary, error FROM scans`
	args := []any{}
	if workspace != "" {
		query += ` WHERE workspace = ?`
		args = append(args, workspace)
	}
	query += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []ScanRecord{}
	for rows.Next() {
		var rec ScanRecord
		var started, finished, summary string
		if err := rows.Scan(&rec.ID, &rec.Workspace, &rec.Status, &started, &finished, &rec.DurationMs, &summary, &rec.Error); err != nil {
			return nil, err
		}
		rec.StartedAt, _ = time.Parse(timeFormat, started)
		if finished != "" {
			rec.FinishedAt, _ = time.Parse(timeFormat, finished)
		}
		if err := json.Unmarshal([]byte(summary), &rec.Summary); err != nil {
			return nil, err
		}
		rec.HasDrift = (&model.Report{Summary: rec.Summary}).HasDrift()
		list = append(list, rec)
	}
	return list, rows.Err()
}

// FailInterrupted marks scans left "running" by a previous process as failed.
func (s *Store) FailInterrupted(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM scans WHERE status = ?`, model.StatusRunning)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		r, err := s.GetScan(ctx, id)
		if err != nil {
			return 0, err
		}
		r.Status, r.Error = model.StatusFailed, "interrupted: the server stopped before the scan finished"
		if err := s.SaveScan(ctx, r); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
