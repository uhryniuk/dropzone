package app

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/packagehandler"
	"golang.org/x/term"
)

const version = "v0.0.1-dev"

// errNotReimplemented is kept for commands that haven't been wired to
// real implementations yet. See docs/roadmap.md for the phase schedule.
var errNotReimplemented = errors.New("not yet reimplemented after design pivot; see docs/roadmap.md")

// SetupCommands configures the CLI commands.
func (a *App) SetupCommands() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "dropzone",
		Short:         "Install binaries from signed OCI images",
		SilenceUsage:  true,
		SilenceErrors: false,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// `version` stays lightweight — no init, no disk touch.
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
	rootCmd.AddCommand(a.newPathCommand())

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
	var allowUnsigned bool
	cmd := &cobra.Command{
		Use:   "install <ref>",
		Short: "Install a package from an OCI registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := a.PackageHandler.InstallPackage(cmd.Context(), args[0], packagehandler.InstallOptions{
				AllowUnsigned: allowUnsigned,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "\nInstalled %s %s (%s)\n", result.Name, result.Tag, shortDigest(result.Digest))
			fmt.Fprintf(out, "  Registry: %s\n", result.Registry)
			fmt.Fprintf(out, "  Platform: %s\n", result.Platform)
			fmt.Fprintf(out, "  Binary:   %s\n", result.BinaryPath)
			if result.SignatureVerified {
				fmt.Fprintf(out, "  Signed by: %s\n", result.Signer)
				if result.Issuer != "" {
					fmt.Fprintf(out, "  Issuer:    %s\n", result.Issuer)
				}
				if a := result.Attestations; a.HasAny() {
					fmt.Fprintln(out, "  Attestations:")
					if a.SBOM != nil {
						fmt.Fprintf(out, "    SBOM:        %s (%d components)\n", strings.ToUpper(a.SBOM.Format), a.SBOM.ComponentCount)
					}
					if a.Provenance != nil {
						label := a.Provenance.BuilderID
						if label == "" {
							label = a.Provenance.BuildType
						}
						fmt.Fprintf(out, "    Provenance:  %s\n", label)
					}
					if a.VulnScan != nil {
						scanned := ""
						if !a.VulnScan.ScannedAt.IsZero() {
							scanned = " (scanned " + a.VulnScan.ScannedAt.Format("2006-01-02") + ")"
						}
						fmt.Fprintf(out, "    Vuln scan:   %dC / %dH / %dM / %dL%s\n",
							a.VulnScan.Critical, a.VulnScan.High, a.VulnScan.Medium, a.VulnScan.Low, scanned)
					}
				}
			} else {
				fmt.Fprintln(out, "  Signature: not verified (--allow-unsigned)")
			}
			if !onPath(a.HostIntegrator.BinPath()) {
				fmt.Fprintf(out, "\nNote: %s is not on your PATH. Run `dz path setup` to configure your shell.\n", a.HostIntegrator.BinPath())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&allowUnsigned, "allow-unsigned", false, "Install from a registry that has no cosign policy configured (or whose images have no signature). A registry with a policy that fails verification cannot be bypassed.")
	return cmd
}

func (a *App) newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			pkgs, err := a.PackageHandler.ListInstalled()
			if err != nil {
				return err
			}
			if len(pkgs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No packages installed.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTAG\tDIGEST\tREGISTRY\tINSTALLED")
			for _, p := range pkgs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					p.Name, p.Tag, shortDigest(p.Digest), p.Registry, p.InstalledAt.Format("2006-01-02 15:04"))
			}
			return w.Flush()
		},
	}
}

func (a *App) newRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an installed package",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.PackageHandler.RemovePackage(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", args[0])
			return nil
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
			return runLogin(a, cmd, args[0], username, password, passwordStdin)
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

func (a *App) newPathCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "path",
		Short: "Manage shell PATH integration for ~/.dropzone/bin",
		Long: `Report or configure whether ~/.dropzone/bin is on your shell PATH.

With no subcommand, prints the current status. Use 'dz path setup' to
append a PATH export to your shell's rc file, and 'dz path unset' to
remove it. Both subcommands are idempotent.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPathStatus(a, cmd)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "setup",
		Short: "Append dropzone PATH export to your shell rc file",
		RunE: func(cmd *cobra.Command, args []string) error {
			wrote, rc, err := a.HostIntegrator.SetupShellRC()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if wrote {
				fmt.Fprintf(out, "Added dropzone PATH block to %s.\n", rc)
				fmt.Fprintf(out, "Run `source %s` or open a new shell to pick it up.\n", rc)
			} else {
				fmt.Fprintf(out, "Already configured in %s (no changes).\n", rc)
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "unset",
		Short: "Remove dropzone's PATH block from your shell rc file",
		RunE: func(cmd *cobra.Command, args []string) error {
			removed, rc, err := a.HostIntegrator.UnsetShellRC()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if removed {
				fmt.Fprintf(out, "Removed dropzone PATH block from %s.\n", rc)
				fmt.Fprintf(out, "Run `source %s` or open a new shell for the change to take effect.\n", rc)
			} else {
				fmt.Fprintf(out, "No dropzone PATH block found in %s (nothing to remove).\n", rc)
			}
			return nil
		},
	})
	return cmd
}

func runPathStatus(a *App, cmd *cobra.Command) error {
	status := a.HostIntegrator.PathStatus()
	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "bin dir:       %s\n", status.BinDir)
	if status.OnPath {
		fmt.Fprintln(out, "on PATH:       yes")
	} else {
		fmt.Fprintln(out, "on PATH:       no")
	}
	if status.DetectedShell == "" {
		fmt.Fprintln(out, "shell:         unknown ($SHELL not bash or zsh)")
	} else {
		fmt.Fprintf(out, "shell:         %s\n", status.DetectedShell)
		fmt.Fprintf(out, "rc file:       %s\n", status.RCFile)
		if status.RCBlockInstalled {
			fmt.Fprintln(out, "rc block:      present")
		} else {
			fmt.Fprintln(out, "rc block:      absent (run `dz path setup` to add)")
		}
	}
	return nil
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
	fmt.Fprintln(out, "Note: credentials are saved but not verified. They will be used on the next install or pull from this registry.")
	return nil
}

// shortDigest truncates an OCI digest to "sha256:12chars" for display.
// Anywhere we need the full digest (metadata, comparison) uses the full
// form; this helper is strictly for human-readable output.
func shortDigest(d string) string {
	if i := strings.Index(d, ":"); i >= 0 && len(d) > i+13 {
		return d[:i+13]
	}
	return d
}

// onPath delegates to hostintegration.OnPath. Tiny helper kept inside
// this package so most of commands.go doesn't care about the import path.
func onPath(dir string) bool {
	return hostintegration.OnPath(dir)
}
