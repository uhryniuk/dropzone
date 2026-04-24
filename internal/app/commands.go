package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const version = "v0.0.1-dev"

// errNotReimplemented is returned by every command whose new implementation
// has not landed yet. See docs/roadmap.md for the phase schedule.
var errNotReimplemented = errors.New("not yet reimplemented after design pivot; see docs/roadmap.md")

// SetupCommands configures the CLI commands.
func (a *App) SetupCommands() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "dropzone",
		Short: "Install binaries from signed OCI images",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// version is the only command that must work right after Phase 0.
			// Everything else is stubbed and doesn't need App.Initialize() yet.
			if cmd.Name() == "version" {
				return nil
			}
			return a.Initialize()
		},
	}

	rootCmd.AddCommand(a.newVersionCommand())
	rootCmd.AddCommand(a.newInstallCommand())
	rootCmd.AddCommand(a.newListCommand())
	rootCmd.AddCommand(a.newRemoveCommand())
	rootCmd.AddCommand(a.newUpdateCommand())
	rootCmd.AddCommand(a.newSearchCommand())
	rootCmd.AddCommand(a.newTagsCommand())
	rootCmd.AddCommand(a.newAddCommand())
	rootCmd.AddCommand(a.newLoginCommand())
	rootCmd.AddCommand(a.newLogoutCommand())

	return rootCmd
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the dropzone version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "dropzone %s\n", version)
		},
	}
}

func (a *App) newInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install <ref>",
		Short: "Install a package from an OCI registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotReimplemented
		},
	}
}

func (a *App) newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotReimplemented
		},
	}
}

func (a *App) newRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotReimplemented
		},
	}
}

func (a *App) newUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update [<name>]",
		Short: "Check installed packages against their source registries",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotReimplemented
		},
	}
}

func (a *App) newSearchCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "search [<term>]",
		Short: "List images available in a registry",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotReimplemented
		},
	}
}

func (a *App) newTagsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tags <image>",
		Short: "List available tags for an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotReimplemented
		},
	}
}

func (a *App) newAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add resources (registries)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "registry <name> <url>",
		Short: "Register an OCI registry as a package source",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotReimplemented
		},
	})
	return cmd
}

func (a *App) newLoginCommand() *cobra.Command {
	var (
		username      string
		password      string
		passwordStdin bool
	)
	cmd := &cobra.Command{
		Use:   "login <registry>",
		Short: "Save credentials for a private OCI registry",
		Long: `Save username/password for a private OCI registry. Credentials are
written to ~/.dropzone/auth.json in the same format as Docker's
config.json. The dropzone client checks this file first and falls back
to the Docker keychain, so 'docker login' still works too.

With --password-stdin, read the password from stdin (for scripting).
Without --password, you'll be prompted.

Examples:
  dz login registry.mycorp.example -u alice
  echo $TOKEN | dz login ghcr.io -u alice --password-stdin`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registryURL := args[0]
			return runLogin(a, cmd, registryURL, username, password, passwordStdin)
		},
	}
	cmd.Flags().StringVarP(&username, "username", "u", "", "Username")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Password (prompted if omitted)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "Read password from stdin")
	return cmd
}

func (a *App) newLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <registry>",
		Short: "Remove saved credentials for a registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registryURL := args[0]
			existed, err := a.AuthStore.Delete(registryURL)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if existed {
				fmt.Fprintf(out, "Removed credentials for %s\n", registryURL)
			} else {
				fmt.Fprintf(out, "No saved credentials for %s\n", registryURL)
			}
			return nil
		},
	}
}

// runLogin is split out so tests can exercise the flow without constructing
// a full cobra Command tree.
func runLogin(a *App, cmd *cobra.Command, registryURL, username, password string, passwordStdin bool) error {
	in := cmd.InOrStdin()
	out := cmd.OutOrStdout()
	// One reader across both prompts — separate bufio.Readers would drop
	// buffered input between reads.
	reader := bufio.NewReader(in)

	if username == "" {
		fmt.Fprintf(out, "Username: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read username: %w", err)
		}
		username = strings.TrimSpace(line)
	}
	if username == "" {
		return errors.New("username is required")
	}

	if passwordStdin {
		data, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("read password from stdin: %w", err)
		}
		password = strings.TrimRight(string(data), "\r\n")
	} else if password == "" {
		// Read from the controlling terminal without echoing. Falls back
		// to a plain ReadString when stdin isn't a tty (e.g., in tests).
		if fd := int(syscall.Stdin); term.IsTerminal(fd) {
			fmt.Fprintf(out, "Password: ")
			b, err := term.ReadPassword(fd)
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			fmt.Fprintln(out)
			password = string(b)
		} else {
			line, err := reader.ReadString('\n')
			if err != nil {
				return fmt.Errorf("read password: %w", err)
			}
			password = strings.TrimRight(line, "\r\n")
		}
	}
	if password == "" {
		return errors.New("password is required")
	}

	if err := a.AuthStore.Save(registryURL, username, password); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved credentials for %s (user %s)\n", registryURL, username)
	// Heads-up, not validation. A wrong password surfaces on the first
	// install; skipping the ping keeps login simple and offline-friendly.
	fmt.Fprintln(out, "Note: credentials are saved but not verified. They will be used on the next install or pull from this registry.")
	return nil
}
