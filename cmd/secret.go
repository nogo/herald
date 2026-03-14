package cmd

import (
	"fmt"
	"os"

	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
)

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage encrypted secrets",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var secretSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a secret",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, value := args[0], args[1]
		store := secrets.NewStore(dataDir)
		if err := store.Init(); err != nil {
			return err
		}
		if err := store.Set(key, value); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "secret '%s' set\n", key)
		return nil
	},
}

var secretGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a secret",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		store := secrets.NewStore(dataDir)
		val, err := store.Get(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprint(cmd.OutOrStdout(), val)
		return nil
	},
}

var secretListCmd = &cobra.Command{
	Use:   "list",
	Short: "List secrets",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store := secrets.NewStore(dataDir)
		keys, err := store.List()
		if err != nil {
			return err
		}
		for _, k := range keys {
			fmt.Fprintln(cmd.OutOrStdout(), k)
		}
		return nil
	},
}

var secretImportCmd = &cobra.Command{
	Use:   "import <key> <file>",
	Short: "Import a file as a secret",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key, filePath := args[0], args[1]
		store := secrets.NewStore(dataDir)
		if err := store.Init(); err != nil {
			return err
		}

		info, err := os.Stat(filePath)
		if err != nil {
			return fmt.Errorf("cannot read file: %w", err)
		}

		if err := store.Import(key, filePath); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "secret '%s' imported from %s (%d bytes)\n", key, filePath, info.Size())
		return nil
	},
}

var secretDeleteCmd = &cobra.Command{
	Use:   "delete <key>",
	Short: "Delete a secret",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		store := secrets.NewStore(dataDir)
		if err := store.Delete(key); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "secret '%s' deleted\n", key)
		return nil
	},
}

func init() {
	secretCmd.AddCommand(secretSetCmd, secretGetCmd, secretListCmd, secretImportCmd, secretDeleteCmd)
	rootCmd.AddCommand(secretCmd)
}
