package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nogo/herald/internal/secrets"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var secretCmd = &cobra.Command{
	Use:   "secret",
	Short: "Manage encrypted secrets",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

var secretSetCmd = &cobra.Command{
	Use:   "set <key> [value]",
	Short: "Set a secret (prompts interactively if value omitted)",
	Long: `Set a secret in the encrypted store.

If value is provided as an argument, it will be used directly.
WARNING: CLI arguments are visible in process listings.

Preferred usage (interactive prompt, input hidden):
  herald secret set mykey

Pipe from stdin:
  echo "my-secret" | herald secret set mykey
  herald secret set mykey < /path/to/secret-file`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		var value string
		if len(args) == 2 {
			value = args[1]
		} else if term.IsTerminal(int(os.Stdin.Fd())) {
			// Interactive: prompt with hidden input
			fmt.Fprintf(cmd.OutOrStdout(), "Enter value for '%s': ", key)
			pass, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(cmd.OutOrStdout()) // newline after hidden input
			if err != nil {
				return fmt.Errorf("reading input: %w", err)
			}
			value = string(pass)
			if value == "" {
				return fmt.Errorf("no value provided")
			}
		} else {
			// Piped stdin
			data, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("reading from stdin: %w", err)
			}
			value = strings.TrimRight(string(data), "\n\r")
			if value == "" {
				return fmt.Errorf("no value provided (pass as argument or pipe to stdin)")
			}
		}
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
			return err
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
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "secret '%s' deleted\n", key)
		return nil
	},
}

func init() {
	secretCmd.AddCommand(secretSetCmd, secretGetCmd, secretListCmd, secretImportCmd, secretDeleteCmd)
	rootCmd.AddCommand(secretCmd)
}
