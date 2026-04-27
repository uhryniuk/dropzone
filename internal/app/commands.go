package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/uhryniuk/dropzone/internal/cosign"
	"github.com/uhryniuk/dropzone/internal/hostintegration"
	"github.com/uhryniuk/dropzone/internal/localstore"
	"github.com/uhryniuk/dropzone/internal/packagehandler"
	"github.com/uhryniuk/dropzone/internal/registry"
	"golang.org/x/term"
)

// writeJSON encodes v as indented JSON to out. Used by --json flags.
func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

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
	rootCmd.AddCommand(a.newRollbackCommand())
	rootCmd.AddCommand(a.newDoctorCommand())
	rootCmd.AddCommand(a.newPurgeCommand())
	// Cobra ships shell completion as a built-in subcommand; enabling
	// it just means not hiding it. Users get
	// `dz completion bash > /etc/bash_completion.d/dz` etc. for free.
	return rootCmd
}

func (a *App) newRollbackCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <name>",
		Short: "Restore a package's previous installation",
		Long: `Flip a package's current symlink to its previous digest dir.
Useful when an update breaks things and you want the old version back
without re-pulling. Only works if a previous digest is still on disk
(i.e., you haven't pruned).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			meta, err := a.PackageHandler.Rollback(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Rolled %s back to %s (%s)\n",
				args[0], meta.Tag, shortDigest(meta.Digest))
			return nil
		},
	}
}

func (a *App) newDoctorCommand() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check dropzone state for inconsistencies",
		Long: `Inspect ~/.dropzone for orphan wrappers, broken current symlinks,
packages without wrappers, and PATH issues. Use --fix to apply the
safe automatic remediations.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := a.PackageHandler.Doctor()
			if err != nil {
				return err
			}
			renderDoctorReport(cmd.OutOrStdout(), report)
			if fix && report.HasIssues() {
				fmt.Fprintln(cmd.OutOrStdout(), "\nApplying safe fixes...")
				report, err = a.PackageHandler.FixDoctor(report)
				if err != nil {
					return err
				}
				if report.HasIssues() {
					fmt.Fprintln(cmd.OutOrStdout(), "Remaining issues after fixes:")
					renderDoctorReport(cmd.OutOrStdout(), report)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "All issues resolved.")
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "Apply automatic remediations for safe-to-fix issues")
	return cmd
}

func renderDoctorReport(out io.Writer, r *packagehandler.DoctorReport) {
	if !r.HasIssues() {
		fmt.Fprintln(out, "Everything looks fine.")
		return
	}
	if len(r.WrapperWithoutPackage) > 0 {
		fmt.Fprintln(out, "Orphan wrappers (no matching package):")
		for _, w := range r.WrapperWithoutPackage {
			fmt.Fprintf(out, "  - ~/.dropzone/bin/%s\n", w)
		}
	}
	if len(r.CurrentSymlinkBroken) > 0 {
		fmt.Fprintln(out, "Broken `current` symlinks:")
		for name, target := range r.CurrentSymlinkBroken {
			fmt.Fprintf(out, "  - %s -> %s (target missing)\n", name, target)
		}
	}
	if len(r.PackagesWithoutCurrent) > 0 {
		fmt.Fprintln(out, "Packages with no current installation:")
		for _, name := range r.PackagesWithoutCurrent {
			fmt.Fprintf(out, "  - %s\n", name)
		}
	}
	if len(r.PackageWithoutWrapper) > 0 {
		fmt.Fprintln(out, "Packages with missing wrapper script:")
		for _, name := range r.PackageWithoutWrapper {
			fmt.Fprintf(out, "  - %s (re-install to regenerate)\n", name)
		}
	}
	if r.PathNotConfigured {
		fmt.Fprintln(out, "PATH:")
		fmt.Fprintln(out, "  - ~/.dropzone/bin is not on $PATH (run `dz path setup`)")
	}
}

func (a *App) newPurgeCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Wipe the entire ~/.dropzone directory",
		Long: `Permanently delete ~/.dropzone, including config, packages,
wrappers, and credentials. The directory is reconstructed on next run.
Shell PATH edits made by 'dz path setup' are NOT removed; run
'dz path unset' first if you want a clean teardown.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			base := a.Config.LocalStorePath
			if !yes && !confirm(cmd, fmt.Sprintf("Delete %s entirely?", base)) {
				fmt.Fprintln(cmd.OutOrStdout(), "Aborted.")
				return nil
			}
			if err := removeDir(base); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Purged %s\n", base)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip the confirmation prompt")
	return cmd
}

// removeDir wraps os.RemoveAll for the purge command. Pulled out so we
// can swap to a "move to trash" implementation later if we want a
// safer purge.
func removeDir(path string) error {
	return removeAll(path)
}

// removeAll is os.RemoveAll plus a guard against passing in something
// suspicious. We refuse to nuke "/" or relative paths -- belt and
// suspenders against a future bug or env quirk.
func removeAll(path string) error {
	if path == "" || path == "/" || !strings.HasPrefix(path, "/") {
		return fmt.Errorf("refusing to purge %q", path)
	}
	return os.RemoveAll(path)
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
			// Config-level always_allow_unsigned promotes to per-install
			// allow-unsigned automatically. The flag still wins where
			// set, but users who never want the prompt can flip the
			// config option once and stop typing the flag.
			result, err := a.PackageHandler.InstallPackage(cmd.Context(), args[0], packagehandler.InstallOptions{
				AllowUnsigned: allowUnsigned || a.Config.AlwaysAllowUnsigned,
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
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed packages",
		RunE: func(cmd *cobra.Command, args []string) error {
			pkgs, err := a.PackageHandler.ListInstalled()
			if err != nil {
				return err
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), pkgs)
			}
			if len(pkgs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No packages installed.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTAG\tDIGEST\tSOURCE\tSIGNED\tINSTALLED")
			for _, p := range pkgs {
				signed := "no"
				if p.SignatureVerified {
					signed = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					p.Name, p.Tag, shortDigest(p.Digest),
					a.packageSource(p), signed,
					p.InstalledAt.Format("2006-01-02 15:04"))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON instead of a table")
	cmd.AddCommand(a.newListRegistriesCommand())
	return cmd
}

func (a *App) newListRegistriesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "registries",
		Short: "List configured registries",
		RunE: func(cmd *cobra.Command, args []string) error {
			regs := a.RegistryManager.List()
			if len(regs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No registries configured.")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tURL\tPOLICY\tDEFAULT")
			for _, r := range regs {
				policy := "none"
				if r.CosignPolicy != nil {
					policy = "custom"
					if r.CosignPolicy.IdentityRegex == "https://github.com/chainguard-images/images/.*" {
						policy = "chainguard"
					}
				}
				def := ""
				if r.Name == a.Config.DefaultRegistry {
					def = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Name, r.URL, policy, def)
			}
			return w.Flush()
		},
	}
}

func (a *App) newRemoveCommand() *cobra.Command {
	cmd := &cobra.Command{
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
	cmd.AddCommand(a.newRemoveRegistryCommand())
	return cmd
}

func (a *App) newRemoveRegistryCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "registry <name>",
		Short: "Unregister an OCI registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Refuse if packages installed from this registry remain --
			// removing the registry would leave them un-updateable.
			pkgs, err := a.PackageHandler.ListInstalled()
			if err != nil {
				return err
			}
			var blocked []string
			for _, p := range pkgs {
				if p.Registry == name {
					blocked = append(blocked, p.Name)
				}
			}
			if len(blocked) > 0 && !force {
				return fmt.Errorf(
					"cannot remove registry %q: packages still installed from it (%s). Remove them first or pass --force",
					name, strings.Join(blocked, ", "))
			}

			if err := a.RegistryManager.Remove(name); err != nil {
				return err
			}
			// If removed registry was the default, drop the default
			// pointer; user can pick a new one.
			if a.Config.DefaultRegistry == name {
				a.Config.DefaultRegistry = ""
				if err := a.Config.Save(a.ConfigPath); err != nil {
					return fmt.Errorf("clear default-registry pointer: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Note: removed registry was the default; set a new default with `dz add registry --default` or by editing config.")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed registry %q\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Remove even if installed packages reference this registry")
	return cmd
}

func (a *App) newUpdateCommand() *cobra.Command {
	var (
		checkOnly     bool
		yes           bool
		all           bool
		allowUnsigned bool
	)
	cmd := &cobra.Command{
		Use:   "update [<name>]",
		Short: "Check installed packages against their source registries",
		Long: `Query each installed package's source registry for digest drift
(same tag, new digest -- typically a CVE-patch rebuild) and newer tags.
Without arguments, scans all installed packages and prints a status
table. With a name, scans just that package and prompts to apply.

  dz update                check all, print status
  dz update --check        same; explicit "do not apply"
  dz update <name>         check name; prompt to apply if drift seen
  dz update --all          apply all available updates after one prompt
  dz update <name> --yes   apply without prompting`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			var infos []packagehandler.UpdateInfo
			if len(args) == 1 && !all {
				info, err := a.PackageHandler.CheckUpdate(ctx, args[0])
				if err != nil {
					return err
				}
				infos = []packagehandler.UpdateInfo{info}
			} else {
				var err error
				infos, err = a.PackageHandler.CheckUpdates(ctx)
				if err != nil {
					return err
				}
			}

			renderUpdateTable(out, infos)

			// Pure-check mode, or nothing to apply.
			anyAvailable := false
			for _, u := range infos {
				if u.HasUpdate() {
					anyAvailable = true
					break
				}
			}
			if checkOnly || !anyAvailable {
				return nil
			}

			// Decide which to apply.
			toApply := infos
			if !all && len(args) == 1 {
				// Single named package; apply just it.
				toApply = []packagehandler.UpdateInfo{infos[0]}
			}

			applicable := []packagehandler.UpdateInfo{}
			for _, u := range toApply {
				if u.HasUpdate() {
					applicable = append(applicable, u)
				}
			}
			if len(applicable) == 0 {
				return nil
			}
			if !yes && !confirm(cmd, fmt.Sprintf("Apply %d update(s)?", len(applicable))) {
				fmt.Fprintln(out, "Aborted.")
				return nil
			}

			opts := packagehandler.InstallOptions{AllowUnsigned: allowUnsigned || a.Config.AlwaysAllowUnsigned}
			for _, u := range applicable {
				targetTag := u.InstalledTag
				if u.SameTagRebuild() {
					// Same tag, new digest: re-install the same tag and
					// the registry will resolve to the new digest.
				} else if len(u.NewerTags) > 0 {
					// Newer tag: pick the lexicographically-greatest
					// available. Users wanting a specific tag can use
					// `dz install <name>:<tag>` directly.
					targetTag = u.NewerTags[len(u.NewerTags)-1]
				}
				fmt.Fprintf(out, "\nUpdating %s -> %s...\n", u.Name, targetTag)
				if _, err := a.PackageHandler.ApplyUpdate(ctx, u.Name, targetTag, opts); err != nil {
					fmt.Fprintf(out, "  failed: %v\n", err)
					continue
				}
				fmt.Fprintf(out, "  ok\n")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "Report status only; do not apply any updates")
	cmd.Flags().BoolVar(&yes, "yes", false, "Skip confirmation prompts")
	cmd.Flags().BoolVar(&all, "all", false, "Apply all available updates")
	cmd.Flags().BoolVar(&allowUnsigned, "allow-unsigned", false, "Allow installs from registries with no cosign policy")
	return cmd
}

func renderUpdateTable(out io.Writer, infos []packagehandler.UpdateInfo) {
	if len(infos) == 0 {
		fmt.Fprintln(out, "No packages installed.")
		return
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tINSTALLED\tAVAILABLE\tREASON")
	for _, u := range infos {
		installed := u.InstalledTag + " (" + shortDigest(u.InstalledDigest) + ")"
		switch {
		case u.UnreachableError != nil:
			fmt.Fprintf(w, "%s\t%s\t<unreachable>\t%v\n", u.Name, installed, u.UnreachableError)
		case u.SameTagRebuild():
			avail := u.InstalledTag + " (" + shortDigest(u.CurrentDigest) + ")"
			extras := ""
			if len(u.NewerTags) > 0 {
				extras = fmt.Sprintf(", newer tags: %s", strings.Join(u.NewerTags, ", "))
			}
			fmt.Fprintf(w, "%s\t%s\t%s\tsame-tag rebuild%s\n", u.Name, installed, avail, extras)
		case len(u.NewerTags) > 0:
			fmt.Fprintf(w, "%s\t%s\t%s\tnewer tag\n", u.Name, installed, strings.Join(u.NewerTags, ", "))
		default:
			fmt.Fprintf(w, "%s\t%s\tup to date\t-\n", u.Name, installed)
		}
	}
	w.Flush()
}

// confirm prompts the user with msg [y/N]; returns true on "y"/"yes".
// Used for one-shot confirmations on update/rollback paths.
func confirm(cmd *cobra.Command, msg string) bool {
	fmt.Fprintf(cmd.OutOrStdout(), "%s [y/N]: ", msg)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func (a *App) newSearchCommand() *cobra.Command {
	var registryName string
	cmd := &cobra.Command{
		Use:   "search [<term>] [--registry <name>]",
		Short: "List images available in a registry",
		Long: `List repositories advertised by a registry's /v2/_catalog endpoint.
Many registries (Docker Hub, GHCR) disable the catalog endpoint; in
those cases search will fail cleanly and you can use 'dz tags <image>'
for a known image instead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			term := ""
			if len(args) == 1 {
				term = args[0]
			}
			if registryName == "" {
				registryName = a.Config.DefaultRegistry
			}
			names, err := a.RegistryManager.Catalog(cmd.Context(), registryName, false)
			if err != nil {
				if errors.Is(err, registry.ErrCatalogUnavailable) {
					fmt.Fprintf(cmd.OutOrStdout(),
						"Registry %q does not expose /v2/_catalog. Use `dz tags <image>` to list tags for a specific image.\n",
						registryName)
					return nil
				}
				return err
			}
			out := cmd.OutOrStdout()
			matched := 0
			for _, n := range names {
				if term == "" || strings.Contains(n, term) {
					fmt.Fprintln(out, n)
					matched++
				}
			}
			if term != "" && matched == 0 {
				fmt.Fprintf(out, "No images matched %q in registry %q.\n", term, registryName)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&registryName, "registry", "", "Registry to search (default: configured default_registry)")
	return cmd
}

func (a *App) newTagsCommand() *cobra.Command {
	var registryName string
	var showAll bool
	cmd := &cobra.Command{
		Use:   "tags <name-or-ref> [--registry <name>]",
		Short: "List available tags for an image",
		Long: `List tags for an image. The argument can be:

  jq                         short name; resolved against the default registry
  chainguard/jq              configured-registry-qualified
  gitea.example.com/owner/x  hostname-qualified (works without dz add registry)

Or, if it matches the name of a package you've already installed,
dropzone uses that package's source registry and image path. So
running 'dz tags crane' after 'dz install gitea.example.com/dilly/crane'
queries gitea.example.com for dilly/crane tags, not the default
registry.

Cosign sidecar tags (signature, attestation, and SBOM artifacts that
look like sha256-<hex>.sig) are hidden by default since you can't
install them. Pass --all to see them too.

Pass --registry to override the argument-derived registry choice.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			arg := args[0]
			reg, image, err := a.resolveTagsTarget(arg, registryName)
			if err != nil {
				return err
			}

			tags, err := a.RegistryManager.Client().Tags(cmd.Context(), reg, image)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			shown := 0
			for _, t := range tags {
				if !showAll && packagehandler.IsCosignSidecarTag(t) {
					continue
				}
				fmt.Fprintln(out, t)
				shown++
			}
			if shown == 0 {
				fmt.Fprintf(out, "No installable tags for %s/%s.\n", reg.URL, image)
				if !showAll && len(tags) > 0 {
					fmt.Fprintf(out, "(%d cosign sidecar tags hidden; pass --all to see them.)\n", len(tags))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&registryName, "registry", "", "Configured registry name to query, overrides argument-derived resolution")
	cmd.Flags().BoolVar(&showAll, "all", false, "Include cosign sidecar tags (signatures, attestations, SBOMs)")
	return cmd
}

// resolveTagsTarget figures out which (registry, image-path) pair the
// user means when they type `dz tags <arg>`. Order of precedence:
//
//  1. --registry flag plus the bare argument as the image path.
//  2. The argument matches an installed package's Name. Use that
//     package's registry and full image path.
//  3. The argument is itself a fully-qualified ref (short name,
//     configured-registry-qualified, or hostname-qualified). Hand it
//     to Manager.Resolve which understands all three.
//
// Step 2 is the "I just installed crane from a private registry, now I
// want to see what versions exist" case. Without it, dz tags crane
// would query the default registry which probably has no crane.
func (a *App) resolveTagsTarget(arg, registryFlag string) (*registry.Registry, string, error) {
	if registryFlag != "" {
		reg, err := a.RegistryManager.Get(registryFlag)
		if err != nil {
			return nil, "", err
		}
		return reg, arg, nil
	}

	// Step 2: did the user type a name that matches something they've
	// installed? Use the metadata to query the right registry.
	if installed, err := a.PackageHandler.ListInstalled(); err == nil {
		for _, p := range installed {
			if p.Name != arg {
				continue
			}
			reg, err := a.registryFromMetadata(p.Registry)
			if err != nil {
				return nil, "", err
			}
			image := p.Image
			if image == "" {
				image = p.Name
			}
			return reg, image, nil
		}
	}

	// Step 3: parse the arg as a ref. Resolve handles short names
	// (default registry), configured-registry-qualified names, and
	// hostname-qualified URLs.
	resolved, err := a.RegistryManager.Resolve(arg)
	if err != nil {
		return nil, "", err
	}
	return resolved.Registry, resolved.Image, nil
}

// registryFromMetadata returns a Registry struct for a name stored in
// PackageMetadata.Registry. For configured registries, it looks up the
// real entry (with cosign policy, custom URL, etc.). For
// hostname-shaped names (the ephemeral case from a hostname-qualified
// install), it materializes a minimal Registry the same way Resolve and
// the update flow do.
func (a *App) registryFromMetadata(name string) (*registry.Registry, error) {
	if reg, err := a.RegistryManager.Get(name); err == nil {
		return reg, nil
	} else if !strings.ContainsAny(name, ".:") {
		return nil, err
	}
	return &registry.Registry{Name: name, URL: name}, nil
}

func (a *App) newAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add resources (registries)",
	}
	cmd.AddCommand(a.newAddRegistryCommand())
	return cmd
}

func (a *App) newAddRegistryCommand() *cobra.Command {
	var (
		template       string
		identityIssuer string
		identityRegex  string
		makeDefault    bool
	)
	cmd := &cobra.Command{
		Use:   "registry <name> <url>",
		Short: "Register an OCI registry as a package source",
		Long: `Register an OCI registry. URL can be a host ("docker.io") or a
namespace path ("docker.io/chainguard"). Use --template for one of the
common signing providers, or --identity-issuer + --identity-regex for
a custom cosign keyless policy. A registry without a policy will
require --allow-unsigned at install time.

Templates:
  chainguard  fully-formed policy for Chainguard's build pipeline
  github      Issuer pinned to GitHub Actions; supply --identity-regex
  gitlab      Issuer pinned to GitLab; supply --identity-regex
  google      Issuer pinned to Google OIDC; supply --identity-regex

Examples:
  dz add registry mycorp registry.mycorp.example/signed \\
      --template github --identity-regex 'https://github.com/mycorp/.*'
  dz add registry hub-mirror docker.io --default`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, url := args[0], args[1]

			r := registry.Registry{Name: name, URL: url}

			// Build cosign policy if any signature-related flag was given.
			havePolicyArgs := template != "" || identityIssuer != "" || identityRegex != ""
			if havePolicyArgs {
				p, err := buildPolicy(template, identityIssuer, identityRegex)
				if err != nil {
					return err
				}
				if p != nil {
					r.CosignPolicy = &registry.CosignPolicy{
						Issuer:        p.Issuer,
						IdentityRegex: p.IdentityRegex,
					}
				}
			}

			if err := a.RegistryManager.Add(r); err != nil {
				return err
			}
			if makeDefault {
				a.Config.DefaultRegistry = name
				if err := a.Config.Save(a.ConfigPath); err != nil {
					return fmt.Errorf("save default-registry change: %w", err)
				}
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Added registry %q -> %s", name, url)
			if r.CosignPolicy != nil {
				fmt.Fprintf(out, " (signed)")
			} else {
				fmt.Fprintf(out, " (unsigned, requires --allow-unsigned at install)")
			}
			fmt.Fprintln(out)
			if makeDefault {
				fmt.Fprintf(out, "Set %q as the default registry.\n", name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&template, "template", "", "Pre-baked policy: chainguard, github, gitlab, google")
	cmd.Flags().StringVar(&identityIssuer, "identity-issuer", "", "OIDC issuer URL for the cosign policy")
	cmd.Flags().StringVar(&identityRegex, "identity-regex", "", "Regular expression matching the signing identity SAN")
	cmd.Flags().BoolVar(&makeDefault, "default", false, "Also set this registry as the default")
	return cmd
}

// buildPolicy assembles a cosign.Policy from template + override flags.
// Template provides defaults; identity-issuer / identity-regex override
// or fill in. Returns (nil, nil) when no policy fields were given (the
// caller treats that as "no policy").
func buildPolicy(template, issuer, regex string) (*cosign.Policy, error) {
	var p cosign.Policy
	if template != "" {
		t, err := cosign.ApplyTemplate(template)
		if err != nil {
			return nil, err
		}
		p = t
	}
	if issuer != "" {
		p.Issuer = issuer
	}
	if regex != "" {
		p.IdentityRegex = regex
	}
	if p.Issuer == "" && p.IdentityRegex == "" {
		return nil, nil
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid policy (template=%q, issuer=%q, identity_regex=%q): %w",
			template, issuer, regex, err)
	}
	return &p, nil
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

// packageSource renders the full installed-from path for display:
// registry URL joined with the image path. We use the registry's URL
// (e.g., "docker.io/chainguard") rather than its short name
// ("chainguard") so users see exactly where the bits came from.
// Hostname-shaped registries (the ephemeral case) have name == URL,
// so the lookup is a no-op for them. Older metadata without an Image
// field falls back to the package name.
func (a *App) packageSource(p localstore.PackageMetadata) string {
	host := p.Registry
	if reg, err := a.RegistryManager.Get(p.Registry); err == nil && reg.URL != "" {
		host = reg.URL
	}
	image := p.Image
	if image == "" {
		image = p.Name
	}
	return host + "/" + image
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
