package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nogo/herald/internal/secrets"
	"github.com/nogo/herald/internal/services"
	"github.com/spf13/cobra"
)

var (
	updateList    bool
	updateTimeout int
)

var updateCmd = &cobra.Command{
	Use:   "update [service]",
	Short: "Run update script for a managed service",
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true

		store := secrets.NewStore(dataDir)
		mgr := &services.ServiceManager{
			Config:  Cfg,
			Secrets: store,
			DataDir: dataDir,
			Logger:  slog.Default(),
		}

		if updateList {
			return runUpdateList(cmd, mgr)
		}

		if len(args) == 0 {
			return fmt.Errorf("usage: herald update <service> or herald update --list")
		}

		stackName := args[0]
		if _, ok := Cfg.Services[stackName]; !ok {
			return fmt.Errorf("service %q not found in config", stackName)
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(updateTimeout)*time.Minute)
		defer cancel()

		start := time.Now()
		if err := mgr.Update(ctx, stackName); err != nil {
			return fmt.Errorf("update %s: %w", stackName, err)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "update %s: success (%s)\n", stackName, time.Since(start).Round(time.Millisecond))
		return nil
	},
}

func runUpdateList(cmd *cobra.Command, mgr *services.ServiceManager) error {
	infos := mgr.List()
	if len(infos) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No services configured.")
		return nil
	}

	// Calculate column widths.
	nameW, domainW := 4, 6 // min widths for headers
	for _, info := range infos {
		if len(info.Name) > nameW {
			nameW = len(info.Name)
		}
		if len(info.Domain) > domainW {
			domainW = len(info.Domain)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout(), "Available services:")
	for _, info := range infos {
		auto := "auto:no"
		if info.AutoDeploy {
			auto = "auto:yes"
		}
		script := info.UpdateScript
		if script == "" {
			script = "(none)"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %-*s  %-*s  %-8s  %s\n",
			nameW, info.Name,
			domainW, info.Domain,
			auto, script,
		)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(updateCmd)
	updateCmd.Flags().BoolVar(&updateList, "list", false, "List available services and their update scripts")
	updateCmd.Flags().IntVar(&updateTimeout, "timeout", 30, "Update script timeout in minutes")
}
