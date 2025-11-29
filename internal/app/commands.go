package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/uhryniuk/dropzone/internal/config"
	"github.com/uhryniuk/dropzone/internal/util"
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

			auth := config.AuthOptions{
				Username:  username,
				Password:  password,
				Token:     token,
				AccessKey: accessKey,
				SecretKey: secretKey,
			}

			if err := a.CPManager.Add(name, repoType, endpoint, auth); err != nil {
				return err
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

			bArgsMap := make(map[string]string)
			for _, arg := range buildArgs {
				parts := strings.SplitN(arg, "=", 2)
				if len(parts) == 2 {
					bArgsMap[parts[0]] = parts[1]
				}
			}

			envMap := make(map[string]string)
			for _, env := range envVars {
				parts := strings.SplitN(env, "=", 2)
				if len(parts) == 2 {
					envMap[parts[0]] = parts[1]
				}
			}

			return a.PackageHandler.BuildPackage(packageName, dockerfilePath, contextPath, bArgsMap, envMap)
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
			return a.PackageHandler.InstallPackage(packageRef)
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
			return a.PackageHandler.ListPackages(installedOnly, availableOnly, repoName, packageName)
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
			parts := strings.Split(packageRef, ":")
			packageName := parts[0]
			version := ""
			if len(parts) > 1 {
				version = parts[1]
			}
			return a.PackageHandler.RemovePackage(packageName, version)
		},
	}
}

func (a *App) newUpdateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Update package indexes from control planes",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.CPManager.UpdateAll(); err != nil {
				return err
			}
			util.LogInfo("Update complete.")
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
			// Use ListPackages to show available versions
			return a.PackageHandler.ListPackages(false, true, repoName, packageName)
		},
	}
	cmd.Flags().StringVar(&repoName, "repo", "", "Filter by repository")
	return cmd
}
