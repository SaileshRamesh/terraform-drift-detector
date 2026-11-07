// Package scheduler runs workspace scans on their cron schedules.
package scheduler

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/saileshramesh/terraform-drift-detector/internal/runner"
	"github.com/saileshramesh/terraform-drift-detector/internal/store"
)

// resyncInterval picks up schedule changes made outside this process, e.g.
// `tfdrift schedule set` against the same database.
const resyncInterval = time.Minute

type entry struct {
	spec string
	id   cron.EntryID
}

type Scheduler struct {
	cron   *cron.Cron
	store  *store.Store
	runner *runner.Runner

	mu      sync.Mutex
	entries map[string]entry // workspace -> cron entry
}

func New(st *store.Store, rn *runner.Runner) *Scheduler {
	return &Scheduler{cron: cron.New(), store: st, runner: rn, entries: map[string]entry{}}
}

// Start loads schedules and keeps them in sync with the store until ctx ends.
func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.Sync(ctx); err != nil {
		return err
	}
	s.cron.Start()
	go func() {
		t := time.NewTicker(resyncInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.Sync(ctx); err != nil {
					log.Printf("scheduler: sync: %v", err)
				}
			}
		}
	}()
	return nil
}

// Stop stops triggering new scans; running scans are left to the runner.
func (s *Scheduler) Stop() { <-s.cron.Stop().Done() }

// Sync makes the cron entries match the schedules stored on workspaces.
func (s *Scheduler) Sync(ctx context.Context) error {
	wss, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	desired := map[string]string{}
	for _, ws := range wss {
		if ws.Schedule != "" {
			desired[ws.Name] = ws.Schedule
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for name, e := range s.entries {
		if desired[name] != e.spec {
			s.cron.Remove(e.id)
			delete(s.entries, name)
		}
	}
	for name, spec := range desired {
		if _, ok := s.entries[name]; ok {
			continue
		}
		id, err := s.cron.AddFunc(spec, func() { s.trigger(name) })
		if err != nil {
			log.Printf("scheduler: workspace %s: %v", name, err)
			continue
		}
		s.entries[name] = entry{spec: spec, id: id}
	}
	return nil
}

// Next returns when the workspace is next scheduled to scan.
func (s *Scheduler) Next(workspace string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[workspace]
	if !ok {
		return time.Time{}, false
	}
	next := s.cron.Entry(e.id).Next
	if next.IsZero() {
		// Not started yet, or entry just added: compute it directly.
		sched, err := cron.ParseStandard(e.spec)
		if err != nil {
			return time.Time{}, false
		}
		next = sched.Next(time.Now())
	}
	return next, true
}

// trigger re-reads the workspace so a scheduled scan always uses its latest
// settings. Overlapping runs are skipped.
func (s *Scheduler) trigger(name string) {
	ws, err := s.store.GetWorkspace(context.Background(), name)
	if err != nil {
		log.Printf("scheduler: workspace %s: %v", name, err)
		return
	}
	id, err := s.runner.Start(*ws)
	switch {
	case errors.Is(err, runner.ErrAlreadyRunning):
		log.Printf("scheduler: skipping %s: previous scan still running", name)
	case err != nil:
		log.Printf("scheduler: starting scan of %s: %v", name, err)
	default:
		log.Printf("scheduler: started scan %s of workspace %s", id, name)
	}
}
