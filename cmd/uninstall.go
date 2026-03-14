package cmd

import (
	"fmt"
	"os"

	"github.com/nogo/herald/internal/systemd"
	"github.com/spf13/cobra"
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the herald systemd service",
	RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getuid() != 0 {
			fmt.Fprintln(os.Stderr, "Uninstalling systemd service requires root. Run: sudo herald uninstall")
			os.Exit(1)
		}

		if err := systemd.Uninstall(); err != nil {
			return err
		}

		fmt.Println("Herald service uninstalled. Data and config at /etc/herald/ were NOT removed.")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(uninstallCmd)
}
