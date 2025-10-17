package app

import (
	"fmt"
	"os"

	"github.com/dropzone/internal/util"
	"github.com/spf13/cobra"
)

// SetupCommands configures the CLI commands.
func (a *App) SetupCommands() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "dropzone",
		Short: "A decentralized meta-package manager built on OCI containers",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Initialize app before running any command
			return a.Initialize()
		},
	}

	rootCmd.AddCommand(a.newAddCommand())
	rootCmd.AddCommand(a.newBuildCommand())
	rootCmd.AddCommand(a.newInstallCommand())
	rootCmd.AddCommand(a.newListCommand())
	rootCmd.AddCommand(a.newRemoveCommand())
	rootCmd.AddCommand(a.newUpdateCommand())
	rootCmd.AddCommand(a.newVersionCommand())
	rootCmd.AddCommand(a.newTagsCommand())

	return rootCmd
}

func (a *App) newAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add resources (e.g., repositories)",
	}
	cmd.AddCommand(a.newAddRepoCommand())
	return cmd
}

func (a *App) newAddRepoCommand() *cobra.Command {
	var username, password, token, accessKey, secretKey string

	cmd := &cobra.Command{
		Use:   "repo <name> <type> <endpoint>",
		Short: "Add a new control plane repository",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			repoType := args[1]
			endpoint := args[2]

			// Validate type (MVP supports OCI)
			if repoType != "oci" && repoType != "github" && repoType != "s3" {
				return fmt.Errorf("unsupported repository type: %s. Supported: oci, github, s3", repoType)
			}

			// In a real implementation, we would construct the auth options
			// and call controlplane.Manager.Add()
			// For CLI foundation, we log the intent.
			util.LogInfo("Adding repo '%s' (%s) at %s", name, repoType, endpoint)
			if username != "" {
				util.LogInfo("With username: %s", username)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&username, "username", "u", "", "Username for authentication")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Password for authentication")
	cmd.Flags().StringVarP(&token, "token", "t", "", "Token for authentication")
	cmd.Flags().StringVar(&accessKey, "access-key", "", "Access Key (S3)")
	cmd.Flags().StringVar(&secretKey, "secret-key", "", "Secret Key (S3)")

	return cmd
}

func (a *App) newBuildCommand() *cobra.Command {
	var contextPath string
	var buildArgs []string
	var envVars []string

	cmd := &cobra.Command{
		Use:   "build <package-name> <dockerfile-path>",
		Short: "Build a care package locally",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageName := args[0]
			dockerfilePath := args[1]
			util.LogInfo("Building package '%s' from %s", packageName, dockerfilePath)
			if contextPath != "" {
				util.LogInfo("Context: %s", contextPath)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&contextPath, "context", ".", "Build context path")
	cmd.Flags().StringArrayVar(&buildArgs, "build-arg", []string{}, "Set build-time variables")
	cmd.Flags().StringArrayVar(&envVars, "env", []string{}, "Set environment variables")

	return cmd
}

func (a *App) newInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install <package-name>[:<tag>]",
		Short: "Install a care package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageRef := args[0]
			util.LogInfo("Installing package '%s'", packageRef)
			return nil
		},
	}
}

func (a *App) newListCommand() *cobra.Command {
	var installedOnly, availableOnly bool
	var repoName, packageName string

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed and available packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			util.LogInfo("Listing packages...")
			if installedOnly {
				util.LogInfo("Filter: Installed only")
			}
			if availableOnly {
				util.LogInfo("Filter: Available only")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&installedOnly, "installed", false, "Show only installed packages")
	cmd.Flags().BoolVar(&availableOnly, "available", false, "Show only available packages")
	cmd.Flags().StringVar(&repoName, "repo", "", "Filter by repository")
	cmd.Flags().StringVar(&packageName, "package", "", "Filter by package name")

	return cmd
}

func (a *App) newRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <package-name>[:<tag>]",
		Short: "Remove an installed care package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageRef := args[0]
			util.LogInfo("Removing package '%s'", packageRef)
			return nil
		},
	}
}

func (a *App) newUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update package indexes from control planes",
		RunE: func(cmd *cobra.Command, args []string) error {
			util.LogInfo("Updating control plane indexes...")
			return nil
		},
	}
}

func (a *App) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version number of dropzone",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "dropzone v0.0.1-mvp\n")
		},
	}
}

func (a *App) newTagsCommand() *cobra.Command {
	var repoName string
	cmd := &cobra.Command{
		Use:   "tags <package-name>",
		Short: "List available tags for a package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			packageName := args[0]
			util.LogInfo("Listing tags for '%s'", packageName)
			return nil
		},
	}
	cmd.Flags().StringVar(&repoName, "repo", "", "Filter by repository")
	return cmd
}
