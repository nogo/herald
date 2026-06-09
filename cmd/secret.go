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
  herald secret set mykey < /path/to/secret-file

Generate a random value (base64, hex, or alphanumeric):
  herald secret set mykey --generate alphanumeric
  herald secret set mykey --generate base64 --length 48
Refuses to overwrite an existing key; pass --force to replace.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cmd.SilenceUsage = true
		key := args[0]
		generate, _ := cmd.Flags().GetString("generate")
		length, _ := cmd.Flags().GetInt("length")
		force, _ := cmd.Flags().GetBool("force")

		store := secrets.NewStore(dataDir)
		if err := store.Init(); err != nil {
			return err
		}

		if generate != "" {
			if len(args) == 2 {
				return fmt.Errorf("--generate is mutually exclusive with a value argument")
			}
			if length < 16 || length > 512 {
				return fmt.Errorf("--length must be between 16 and 512, got %d", length)
			}
			value, err := secrets.GenerateSecret(generate, length)
			if err != nil {
				return err
			}
			if force {
				if err := store.Set(key, value); err != nil {
					return err
				}
			} else {
				written, err := store.SetIfAbsent(key, value)
				if err != nil {
					return err
				}
				if !written {
					return fmt.Errorf("secret %q already exists; pass --force to overwrite", key)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "secret '%s' generated (%s, %d)\n", key, generate, length)
			return nil
		}

		var value string
		if len(args) == 2 {
			value = args[1]
			if value == "" {
				return fmt.Errorf("no value provided")
			}
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
		cmd.SilenceUsage = true
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
		cmd.SilenceUsage = true
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
		cmd.SilenceUsage = true
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
		cmd.SilenceUsage = true
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
	secretSetCmd.Flags().String("generate", "", "Generate a random value: base64, hex, or alphanumeric")
	secretSetCmd.Flags().Int("length", 32, "Length for --generate (16–512)")
	secretSetCmd.Flags().Bool("force", false, "Overwrite the key if it already exists (with --generate)")
	secretCmd.AddCommand(secretSetCmd, secretGetCmd, secretListCmd, secretImportCmd, secretDeleteCmd)
	secretCmd.GroupID = "secrets"
	rootCmd.AddCommand(secretCmd)
}
