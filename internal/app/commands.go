package app

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
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
