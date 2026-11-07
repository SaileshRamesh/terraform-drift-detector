package main

import (
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/report"
	"github.com/saileshramesh/terraform-drift-detector/internal/store"
)

func newReportCmd(g *globalFlags) *cobra.Command {
	var format string
	var noColor bool
	cmd := &cobra.Command{
		Use:   "report <scan-id>",
		Short: "Show a recorded scan report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := g.historyStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()
			rep, err := st.GetScan(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			switch format {
			case "json":
				return report.WriteJSON(out, rep)
			case "console":
				if rep.Status == model.StatusFailed {
					fmt.Fprintf(out, "Scan %s failed: %s\n", rep.ScanID, rep.Error)
					return nil
				}
				return report.WriteConsole(out, rep, report.ConsoleOptions{Color: useColor(out, noColor)})
			}
			return fmt.Errorf("unknown --format %q: use console or json", format)
		},
	}
	cmd.Flags().StringVarP(&format, "format", "f", "console", "console or json")
	cmd.Flags().BoolVar(&noColor, "no-color", false, "disable coloured console output")
	return cmd
}

func newHistoryCmd(g *globalFlags) *cobra.Command {
	var workspace string
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List recorded scans, newest first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := g.historyStore(cmd)
			if err != nil {
				return err
			}
			defer st.Close()
			scans, err := st.ListScans(cmd.Context(), workspace, limit)
			if err != nil {
				return err
			}
			if len(scans) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No scans recorded.")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SCAN ID\tWORKSPACE\tSTARTED\tSTATUS\tDELETED\tMODIFIED\tTAGS\tUNMANAGED\tERRORS")
			for _, s := range scans {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
					s.ID, s.Workspace, s.StartedAt.Local().Format("2006-01-02 15:04"), scanStatus(s),
					s.Summary.Deleted, s.Summary.Modified, s.Summary.TagsChanged, s.Summary.Unmanaged, s.Summary.Errors)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVarP(&workspace, "workspace", "w", "", "only this workspace")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "number of scans")
	return cmd
}

func scanStatus(s store.ScanRecord) string {
	switch {
	case s.Status != model.StatusCompleted:
		return s.Status
	case s.HasDrift:
		return "drift"
	case s.Summary.Errors > 0:
		return "incomplete" // nothing drifted, but some resources were not checked
	}
	return "in sync"
}

// historyStore opens the database for commands that only make sense with
// one, defaulting to ./tfdrift.db.
func (g *globalFlags) historyStore(cmd *cobra.Command) (*store.Store, error) {
	cfg, err := g.loadConfig()
	if err != nil {
		return nil, err
	}
	st, err := g.openStore(cmd.Context(), cfg, defaultDatabase)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("no database configured")
	}
	return st, nil
}
