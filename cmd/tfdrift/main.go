// Command tfdrift detects drift between Terraform state and live cloud
// infrastructure without running terraform plan.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// exitCode lets a command choose the process exit status without printing an
// error, e.g. exit 2 when drift is found and --fail-on-drift is set.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:           "tfdrift",
		Short:         "Detect drift between Terraform state and live cloud infrastructure",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	g := &globalFlags{}
	root.PersistentFlags().StringVarP(&g.configPath, "config", "c", "", "config file (default ./drift.yaml if present)")
	root.PersistentFlags().StringVar(&g.dbPath, "db", "", "SQLite database for workspaces and scan history (default from config)")
	root.AddCommand(
		newScanCmd(g), newServeCmd(g), newReportCmd(g), newHistoryCmd(g),
		newWorkspaceCmd(g), newScheduleCmd(g), newInventoryCmd(g), newHandlersCmd(),
	)
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), version) },
	})

	err := root.ExecuteContext(ctx)
	var code exitCode
	switch {
	case err == nil:
	case errors.As(err, &code):
		os.Exit(int(code))
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
