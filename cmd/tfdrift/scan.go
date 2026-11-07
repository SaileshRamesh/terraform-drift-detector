package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/saileshramesh/terraform-drift-detector/internal/config"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/report"
	"github.com/saileshramesh/terraform-drift-detector/internal/runner"
	"github.com/saileshramesh/terraform-drift-detector/internal/store"
)

type scanFlags struct {
	workspace       string
	states          []string
	stateRegion     string
	region          string
	profile         string
	format          string
	out             string
	concurrency     int
	failOnDrift     bool
	noColor         bool
	showSkipped     bool
	detectUnmanaged bool
}

func newScanCmd(g *globalFlags) *cobra.Command {
	var f scanFlags
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Compare Terraform state against live infrastructure",
		Long: `Compare Terraform state against live infrastructure.

Scans the default workspace (top-level sources in drift.yaml), a named
workspace (--workspace), or ad-hoc state files (--state). When a database is
configured the result is recorded in scan history.`,
		Example: `  tfdrift scan --state ./terraform.tfstate --region us-east-1
  tfdrift scan --state s3://my-tf-state/prod/terraform.tfstate --format json
  tfdrift scan --workspace prod --out drift-report.json --fail-on-drift
  tfdrift scan --state ./terraform.tfstate --detect-unmanaged`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runScan(cmd.Context(), cmd.OutOrStdout(), g, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.workspace, "workspace", "w", "", "workspace to scan, from the config file or database")
	fl.StringArrayVarP(&f.states, "state", "s", nil, "state file path, s3://bucket/key or http(s) URL; repeatable, replaces the workspace sources")
	fl.StringVar(&f.stateRegion, "state-region", "", "region of S3 state buckets given with --state (default: --region)")
	fl.StringVar(&f.region, "region", "", "default AWS region")
	fl.StringVar(&f.profile, "profile", "", "AWS shared config profile")
	fl.StringVarP(&f.format, "format", "f", "console", "stdout format: console or json")
	fl.StringVarP(&f.out, "out", "o", "", "also write the JSON report to this file")
	fl.IntVar(&f.concurrency, "concurrency", 0, "parallel fetch groups (default 8)")
	fl.BoolVar(&f.failOnDrift, "fail-on-drift", false, "exit with status 2 when drift is detected")
	fl.BoolVar(&f.noColor, "no-color", false, "disable coloured console output")
	fl.BoolVar(&f.showSkipped, "show-skipped", false, "list each resource whose type is not supported yet")
	fl.BoolVar(&f.detectUnmanaged, "detect-unmanaged", false, "also report cloud resources that no scanned state manages (lists every resource of the managed types)")
	return cmd
}

func runScan(ctx context.Context, stdout io.Writer, g *globalFlags, f scanFlags) error {
	if f.format != "console" && f.format != "json" {
		return fmt.Errorf("unknown --format %q: use console or json", f.format)
	}
	cfg, err := g.loadConfig()
	if err != nil {
		return err
	}
	st, err := g.openStore(ctx, cfg, "")
	if err != nil {
		return err
	}
	if st != nil {
		defer st.Close()
	}
	ws, err := scanWorkspace(ctx, cfg, st, f)
	if err != nil {
		return err
	}

	concurrency := f.concurrency
	if concurrency <= 0 {
		concurrency = cfg.Concurrency
	}
	rep, err := runner.New(ctx, st, concurrency).Run(ctx, ws)
	if err != nil {
		return err
	}

	if f.out != "" {
		if err := writeJSONFile(f.out, rep); err != nil {
			return err
		}
	}
	if f.format == "json" {
		err = report.WriteJSON(stdout, rep)
	} else {
		err = report.WriteConsole(stdout, rep, report.ConsoleOptions{
			Color:       useColor(stdout, f.noColor),
			ShowSkipped: f.showSkipped,
		})
	}
	if err != nil {
		return err
	}
	if f.failOnDrift && rep.HasDrift() {
		return exitCode(2)
	}
	return nil
}

// scanWorkspace picks the workspace to scan and applies flag overrides.
func scanWorkspace(ctx context.Context, cfg loadedConfig, st *store.Store, f scanFlags) (config.Workspace, error) {
	ws := cfg.DefaultWorkspace()
	if f.workspace != "" {
		var err error
		if ws, err = findWorkspace(ctx, cfg, st, f.workspace); err != nil {
			return ws, err
		}
	}
	if len(f.states) > 0 {
		ws.Sources = nil
		for _, s := range f.states {
			src, err := config.ParseSource(s)
			if err != nil {
				return ws, err
			}
			if src.S3 != nil {
				src.S3.Region = f.stateRegion
			}
			ws.Sources = append(ws.Sources, src)
		}
	}
	if f.region != "" {
		ws.AWS.Region = f.region
	}
	if f.profile != "" {
		ws.AWS.Profile = f.profile
	}
	if f.detectUnmanaged {
		ws.DetectUnmanaged = true
	}
	if len(ws.Sources) == 0 {
		return ws, errors.New("no state sources: pass --state or --workspace, or list sources in drift.yaml")
	}
	return ws, nil
}

func writeJSONFile(path string, rep *model.Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := report.WriteJSON(file, rep); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func useColor(w io.Writer, disabled bool) bool {
	return !disabled && os.Getenv("NO_COLOR") == "" && isTerminal(w)
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
