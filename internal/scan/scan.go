// Package scan orchestrates one drift scan: read state, extract the expected
// model, fetch the actual model, diff, and summarize.
package scan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/engine"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/state"
)

type Options struct {
	// ScanID is generated when empty.
	ScanID        string
	Readers       []state.Reader
	Registry      *cloud.Registry
	Differ        *engine.Differ
	DefaultRegion string
	// Concurrency bounds how many (type, region) groups are fetched at once.
	Concurrency int
	// DetectUnmanaged lists every resource of the managed types in the
	// scanned regions and reports those absent from all scanned states.
	DetectUnmanaged bool
}

// Run always returns a report. When the scan cannot complete (e.g. a state
// file is unreadable) the report has status "failed" and err is set.
func Run(ctx context.Context, opts Options) (*model.Report, error) {
	report := &model.Report{
		SchemaVersion: model.SchemaVersion,
		ScanID:        opts.ScanID,
		Status:        model.StatusRunning,
		StartedAt:     time.Now().UTC(),
		Drifts:        []model.Drift{},
	}
	if report.ScanID == "" {
		report.ScanID = NewScanID()
	}
	err := run(ctx, opts, report)
	report.FinishedAt = time.Now().UTC()
	report.DurationMs = report.FinishedAt.Sub(report.StartedAt).Milliseconds()
	if err != nil {
		report.Status, report.Error = model.StatusFailed, err.Error()
		return report, err
	}
	report.Status = model.StatusCompleted
	return report, nil
}

// Failed builds the report for a scan that could not start.
func Failed(scanID string, err error) *model.Report {
	now := time.Now().UTC()
	return &model.Report{
		SchemaVersion: model.SchemaVersion,
		ScanID:        scanID,
		Status:        model.StatusFailed,
		Error:         err.Error(),
		StartedAt:     now,
		FinishedAt:    now,
		Drifts:        []model.Drift{},
	}
}

func run(ctx context.Context, opts Options, report *model.Report) error {
	var expected []*model.Resource
	seen := make(map[string]bool)
	resources := 0
	for _, r := range opts.Readers {
		data, err := r.Read(ctx)
		if err != nil {
			return fmt.Errorf("reading state %s: %w", r.Source(), err)
		}
		f, err := state.Parse(data)
		if err != nil {
			return fmt.Errorf("parsing state %s: %w", r.Source(), err)
		}
		report.Sources = append(report.Sources, r.Source())

		extracted, problems := state.Extract(f, opts.Registry, opts.DefaultRegion)
		report.Drifts = append(report.Drifts, problems...)
		resources += len(problems)
		for _, res := range extracted {
			if seen[res.Key()] {
				continue
			}
			seen[res.Key()] = true
			expected = append(expected, res)
		}
	}
	resources += len(expected)

	actual, fetchErrs := fetchAll(ctx, opts, expected)
	for _, exp := range expected {
		if err, failed := fetchErrs[groupKey(exp.Type, exp.Region)]; failed {
			report.Drifts = append(report.Drifts, model.Drift{
				Address: exp.Address, Type: exp.Type, ID: exp.ID, Region: exp.Region,
				Kind: model.Error, Severity: model.Medium, Message: "fetching from cloud: " + err.Error(),
			})
			continue
		}
		report.Drifts = append(report.Drifts, opts.Differ.Compare(exp, actual[exp.Key()])...)
	}
	if opts.DetectUnmanaged {
		report.Drifts = append(report.Drifts, findUnmanaged(ctx, opts, expected, seen)...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	sortDrifts(report.Drifts)
	report.Summary = summarize(resources, report.Drifts)
	return nil
}

type fetchGroup struct {
	handler cloud.Handler
	region  string
	ids     []string
}

func groupKey(tfType, region string) string { return tfType + "@" + region }

// fetchAll batches lookups per (type, region) and runs the groups
// concurrently. A failed group is reported per resource instead of aborting
// the whole scan, so one missing IAM permission does not hide other drift.
func fetchAll(ctx context.Context, opts Options, expected []*model.Resource) (map[string]*model.Resource, map[string]error) {
	groups := make(map[string]*fetchGroup)
	for _, res := range expected {
		k := groupKey(res.Type, res.Region)
		g, ok := groups[k]
		if !ok {
			h, _ := opts.Registry.Get(res.Type)
			g = &fetchGroup{handler: h, region: res.Region}
			groups[k] = g
		}
		g.ids = append(g.ids, res.ID)
	}

	var mu sync.Mutex
	actual := make(map[string]*model.Resource, len(expected))
	errs := make(map[string]error)

	var eg errgroup.Group
	eg.SetLimit(max(opts.Concurrency, 1))
	for k, g := range groups {
		eg.Go(func() error {
			found, err := g.handler.FetchActual(ctx, g.region, g.ids)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[k] = err
				return nil
			}
			for id, res := range found {
				res.Provider, res.Type = g.handler.Provider(), g.handler.Type()
				if res.Region == "" {
					res.Region = g.region
				}
				actual[model.Key(g.handler.Type(), id)] = res
			}
			return nil
		})
	}
	_ = eg.Wait()
	return actual, errs
}

// findUnmanaged lists each (type, region) that appears in state, for handlers
// that support listing, and reports ids that no scanned state manages.
func findUnmanaged(ctx context.Context, opts Options, expected []*model.Resource, managed map[string]bool) []model.Drift {
	type target struct {
		lister         cloud.Lister
		tfType, region string
	}
	targets := map[string]target{}
	for _, res := range expected {
		h, _ := opts.Registry.Get(res.Type)
		if l, ok := h.(cloud.Lister); ok {
			targets[groupKey(res.Type, res.Region)] = target{l, res.Type, res.Region}
		}
	}

	var mu sync.Mutex
	var drifts []model.Drift
	var eg errgroup.Group
	eg.SetLimit(max(opts.Concurrency, 1))
	for _, t := range targets {
		eg.Go(func() error {
			ids, err := t.lister.ListIDs(ctx, t.region)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				drifts = append(drifts, model.Drift{
					Type: t.tfType, Region: t.region, Kind: model.Error, Severity: model.Medium,
					Message: "listing resources for unmanaged detection: " + err.Error(),
				})
				return nil
			}
			for _, id := range ids {
				if !managed[model.Key(t.tfType, id)] {
					drifts = append(drifts, model.Drift{
						Type: t.tfType, ID: id, Region: t.region, Kind: model.Unmanaged, Severity: model.Medium,
						Message: "exists in the cloud but is not in any scanned Terraform state",
					})
				}
			}
			return nil
		})
	}
	_ = eg.Wait()
	return drifts
}

func summarize(total int, drifts []model.Drift) model.Summary {
	s := model.Summary{Resources: total}
	drifted := make(map[string]bool)
	for _, d := range drifts {
		switch d.Kind {
		case model.Deleted:
			s.Deleted++
		case model.Modified:
			s.Modified++
		case model.TagsChanged:
			s.TagsChanged++
		case model.Unmanaged:
			s.Unmanaged++
		case model.Error:
			s.Errors++
		case model.Skipped:
			s.Skipped++
		}
		// Unmanaged resources and listing errors are not part of the state, so
		// they do not affect the in-sync count.
		if d.Kind != model.Skipped && d.Kind != model.Unmanaged && d.Address != "" {
			drifted[d.Address] = true
		}
	}
	s.InSync = total - s.Skipped - len(drifted)
	return s
}

var kindOrder = map[model.DriftKind]int{
	model.Deleted: 0, model.Modified: 1, model.TagsChanged: 2, model.Unmanaged: 3, model.Error: 4, model.Skipped: 5,
}

func sortDrifts(drifts []model.Drift) {
	sort.SliceStable(drifts, func(i, j int) bool {
		a, b := drifts[i], drifts[j]
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Address != b.Address {
			return a.Address < b.Address
		}
		return a.Type+a.ID < b.Type+b.ID
	})
}

// NewScanID returns a sortable, unique scan id.
func NewScanID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}
