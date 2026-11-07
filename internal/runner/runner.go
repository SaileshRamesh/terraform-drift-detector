// Package runner executes workspace scans and records them in the store. It
// is shared by the CLI, the API server and the scheduler.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/cloud/awscloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/config"
	"github.com/saileshramesh/terraform-drift-detector/internal/engine"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/scan"
	"github.com/saileshramesh/terraform-drift-detector/internal/state"
	"github.com/saileshramesh/terraform-drift-detector/internal/store"
)

var ErrAlreadyRunning = errors.New("a scan is already running for this workspace")

const defaultConcurrency = 8

type Runner struct {
	// base bounds background scans; cancelling it aborts them.
	base        context.Context
	store       *store.Store
	concurrency int

	mu      sync.Mutex
	running map[string]string // workspace -> scan id
	wg      sync.WaitGroup
}

// New returns a runner. st may be nil, in which case scans are not recorded.
func New(base context.Context, st *store.Store, concurrency int) *Runner {
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	return &Runner{base: base, store: st, concurrency: concurrency, running: map[string]string{}}
}

// Run scans ws and waits for the result. On failure the returned report has
// status "failed" and err is set.
func (r *Runner) Run(ctx context.Context, ws config.Workspace) (*model.Report, error) {
	id, err := r.begin(ws.Name)
	if err != nil {
		return nil, err
	}
	defer r.end(ws.Name)
	return r.execute(ctx, ws, id)
}

// Start launches a scan in the background and returns its id immediately.
// The scan is recorded as "running" right away so it shows up in history.
func (r *Runner) Start(ws config.Workspace) (string, error) {
	id, err := r.begin(ws.Name)
	if err != nil {
		return "", err
	}
	if r.store != nil {
		placeholder := &model.Report{
			SchemaVersion: model.SchemaVersion, ScanID: id, Workspace: ws.Name,
			Status: model.StatusRunning, StartedAt: time.Now().UTC(), Drifts: []model.Drift{},
		}
		if err := r.store.SaveScan(r.base, placeholder); err != nil {
			r.end(ws.Name)
			return "", err
		}
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.end(ws.Name)
		if _, err := r.execute(r.base, ws, id); err != nil {
			log.Printf("scan %s of workspace %s failed: %v", id, ws.Name, err)
		}
	}()
	return id, nil
}

// Running returns the id of the in-progress scan for a workspace, if any.
func (r *Runner) Running(workspace string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.running[workspace]
	return id, ok
}

// Wait blocks until background scans finish.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) begin(workspace string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.running[workspace]; busy {
		return "", ErrAlreadyRunning
	}
	id := scan.NewScanID()
	r.running[workspace] = id
	return id, nil
}

func (r *Runner) end(workspace string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.running, workspace)
}

func (r *Runner) execute(ctx context.Context, ws config.Workspace, id string) (*model.Report, error) {
	rep, err := r.scan(ctx, ws, id)
	rep.Workspace = ws.Name
	if r.store != nil {
		// Record the outcome even if ctx was cancelled mid-scan.
		if serr := r.store.SaveScan(context.WithoutCancel(ctx), rep); serr != nil && err == nil {
			err = fmt.Errorf("saving scan: %w", serr)
		}
	}
	return rep, err
}

func (r *Runner) scan(ctx context.Context, ws config.Workspace, id string) (*model.Report, error) {
	clients, err := awscloud.NewSDKClients(ctx, ws.AWS.Profile, ws.AWS.Region)
	if err != nil {
		return scan.Failed(id, err), err
	}
	return scan.Run(ctx, scan.Options{
		ScanID:          id,
		Readers:         Readers(ws.Sources, clients),
		Registry:        cloud.NewRegistry(awscloud.Handlers(clients)...),
		Differ:          engine.NewDiffer(ws.Ignore),
		DefaultRegion:   clients.DefaultRegion(),
		Concurrency:     r.concurrency,
		DetectUnmanaged: ws.DetectUnmanaged,
	})
}

// Readers builds a state reader for each source.
func Readers(sources []config.Source, clients *awscloud.SDKClients) []state.Reader {
	readers := make([]state.Reader, 0, len(sources))
	for _, s := range sources {
		switch {
		case s.S3 != nil:
			readers = append(readers, state.S3Reader{Client: clients.S3(s.S3.Region), Bucket: s.S3.Bucket, Key: s.S3.Key})
		case s.URL != "":
			readers = append(readers, state.HTTPReader{URL: s.URL})
		default:
			readers = append(readers, state.LocalReader{Path: s.Path})
		}
	}
	return readers
}
