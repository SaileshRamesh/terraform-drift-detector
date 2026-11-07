package main

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/cloud/awscloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/model"
	"github.com/saileshramesh/terraform-drift-detector/internal/runner"
	"github.com/saileshramesh/terraform-drift-detector/internal/state"
)

func newInventoryCmd(g *globalFlags) *cobra.Command {
	var f scanFlags
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Show the expected resource model read from state, without calling the cloud",
		Long: `Show the normalized expected model extracted from Terraform state. No cloud
API is called (S3 state sources still need credentials to download), which
makes this useful for checking what a scan will compare.`,
		Example: `  tfdrift inventory --state ./terraform.tfstate
  tfdrift inventory --workspace prod --format json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
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

			var clients *awscloud.SDKClients
			for _, s := range ws.Sources {
				if s.S3 != nil {
					if clients, err = awscloud.NewSDKClients(ctx, ws.AWS.Profile, ws.AWS.Region); err != nil {
						return err
					}
					break
				}
			}
			registry := cloud.NewRegistry(awscloud.Handlers(nil)...)
			var resources []*model.Resource
			var problems []model.Drift
			for _, r := range runner.Readers(ws.Sources, clients) {
				data, err := r.Read(ctx)
				if err != nil {
					return fmt.Errorf("reading state %s: %w", r.Source(), err)
				}
				file, err := state.Parse(data)
				if err != nil {
					return fmt.Errorf("parsing state %s: %w", r.Source(), err)
				}
				res, probs := state.Extract(file, registry, ws.AWS.Region)
				resources = append(resources, res...)
				problems = append(problems, probs...)
			}

			out := cmd.OutOrStdout()
			if f.format == "json" {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"resources": resources, "not_checked": problems})
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ADDRESS\tTYPE\tID\tREGION\tATTRIBUTES\tTAGS")
			for _, r := range resources {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\n", r.Address, r.Type, r.ID, orDash(r.Region), len(r.Attributes), len(r.Tags))
			}
			for _, p := range problems {
				fmt.Fprintf(tw, "%s\t%s\t-\t-\t%s\t\n", p.Address, p.Type, p.Kind)
			}
			return tw.Flush()
		},
	}
	fl := cmd.Flags()
	fl.StringVarP(&f.workspace, "workspace", "w", "", "workspace whose sources to read")
	fl.StringArrayVarP(&f.states, "state", "s", nil, "state file path, s3://bucket/key or http(s) URL; repeatable")
	fl.StringVar(&f.stateRegion, "state-region", "", "region of S3 state buckets given with --state")
	fl.StringVar(&f.region, "region", "", "default AWS region")
	fl.StringVar(&f.profile, "profile", "", "AWS shared config profile")
	fl.StringVarP(&f.format, "format", "f", "console", "console or json")
	return cmd
}
