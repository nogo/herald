package cmd

import (
	"fmt"
	"maps"
	"slices"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show all services, domains, and health",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := Cfg
		fmt.Printf("Server: %s\n", cfg.Server.Name)
		fmt.Printf("Deploy: %s\n", cfg.Server.DeployDomain)

		if len(cfg.Apps) > 0 {
			fmt.Println("\nApps:")
			for _, name := range slices.Sorted(maps.Keys(cfg.Apps)) {
				app := cfg.Apps[name]
				fmt.Printf("  %-16s %-32s %s:%s\n", name, app.Domain, app.Repo, app.Branch)
			}
		}

		if len(cfg.Stacks) > 0 {
			fmt.Println("\nStacks:")
			for _, name := range slices.Sorted(maps.Keys(cfg.Stacks)) {
				stack := cfg.Stacks[name]
				auto := "no"
				if stack.AutoDeploy {
					auto = "yes"
				}
				fmt.Printf("  %-16s %-32s auto:%s\n", name, stack.Domain, auto)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
