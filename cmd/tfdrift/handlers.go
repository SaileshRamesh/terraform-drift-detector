package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/saileshramesh/terraform-drift-detector/internal/cloud"
	"github.com/saileshramesh/terraform-drift-detector/internal/cloud/awscloud"
)

func newHandlersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "handlers",
		Short: "List the Terraform resource types that are checked for drift",
		Run: func(cmd *cobra.Command, _ []string) {
			for _, t := range cloud.NewRegistry(awscloud.Handlers(nil)...).Types() {
				fmt.Fprintln(cmd.OutOrStdout(), t)
			}
		},
	}
}
