package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/saileshramesh/terraform-drift-detector/internal/config"
)

func newWorkspaceCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"workspaces", "ws"},
		Short:   "List and inspect workspaces",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List workspaces from the config file and database",
			RunE: func(cmd *cobra.Command, _ []string) error {
				st, err := g.historyStore(cmd)
				if err != nil {
					return err
				}
				defer st.Close()
				wss, err := st.ListWorkspaces(cmd.Context())
				if err != nil {
					return err
				}
				if len(wss) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No workspaces. Define them under workspaces: in drift.yaml or create them through the API.")
					return nil
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tMANAGED BY\tSCHEDULE\tREGION\tSOURCES")
				for _, ws := range wss {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ws.Name, ws.ManagedBy, orDash(ws.Schedule), orDash(ws.AWS.Region), describeSources(ws.Sources))
				}
				return tw.Flush()
			},
		},
		&cobra.Command{
			Use:   "show <name>",
			Short: "Print a workspace definition as JSON",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				st, err := g.historyStore(cmd)
				if err != nil {
					return err
				}
				defer st.Close()
				ws, err := st.GetWorkspace(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(ws)
			},
		},
		&cobra.Command{
			Use:   "delete <name>",
			Short: "Delete an API-managed workspace (scan history is kept)",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				st, err := g.historyStore(cmd)
				if err != nil {
					return err
				}
				defer st.Close()
				ws, err := st.GetWorkspace(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				if err := requireAPIManaged(ws); err != nil {
					return err
				}
				return st.DeleteWorkspace(cmd.Context(), ws.Name)
			},
		},
	)
	return cmd
}

func newScheduleCmd(g *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Set or remove a workspace's scan schedule",
		Long: `Set or remove the cron schedule of an API-managed workspace. A running
"tfdrift serve" using the same database picks up the change within a minute.
Schedules of workspaces defined in drift.yaml are edited in that file.`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:     "set <workspace> <cron>",
			Short:   "Set the schedule, e.g. \"0 */6 * * *\" or @hourly",
			Example: `  tfdrift schedule set prod "0 6 * * *"`,
			Args:    cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, args []string) error {
				return updateSchedule(cmd, g, args[0], args[1])
			},
		},
		&cobra.Command{
			Use:   "remove <workspace>",
			Short: "Remove the schedule; the workspace stays available for on-demand scans",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return updateSchedule(cmd, g, args[0], "")
			},
		},
	)
	return cmd
}

func updateSchedule(cmd *cobra.Command, g *globalFlags, name, spec string) error {
	if err := config.ValidateSchedule(spec); err != nil {
		return err
	}
	st, err := g.historyStore(cmd)
	if err != nil {
		return err
	}
	defer st.Close()
	ws, err := st.GetWorkspace(cmd.Context(), name)
	if err != nil {
		return err
	}
	if err := requireAPIManaged(ws); err != nil {
		return err
	}
	ws.Schedule = spec
	return st.UpsertWorkspace(cmd.Context(), *ws)
}

func requireAPIManaged(ws *config.Workspace) error {
	if ws.ManagedBy == config.ManagedByConfig {
		return fmt.Errorf("workspace %q is defined in the config file; edit it there", ws.Name)
	}
	return nil
}

func describeSources(sources []config.Source) string {
	parts := make([]string, len(sources))
	for i, s := range sources {
		switch {
		case s.S3 != nil:
			parts[i] = fmt.Sprintf("s3://%s/%s", s.S3.Bucket, s.S3.Key)
		case s.URL != "":
			parts[i] = s.URL
		default:
			parts[i] = s.Path
		}
	}
	return strings.Join(parts, ", ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
