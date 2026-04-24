# Backburner

Things we've consciously deferred. Not open questions (those live in the relevant feature doc) — things we've decided to come back to, with enough context to pick them up later.

Grouped by theme. Each entry ends with the file that triggered the decision so future-you can find the surrounding discussion.

## Install correctness

- **Setuid preservation during rootfs unpack.** The tar extractor drops setuid/setgid bits. Fine for MVP (CLI tools, user-writable files with setuid are noisy), but binaries that legitimately depend on elevated privileges won't work. Revisit when we hit a real case. See `docs/features/binary_shimming.md` §5.5.
- **Auto-set environment hints.** When the unpacked rootfs contains a recognizable CA bundle (`/etc/ssl/certs/ca-certificates.crt`), timezone database (`/usr/share/zoneinfo`), or locale data (`/usr/lib/locale`), the wrapper could auto-set `SSL_CERT_FILE` / `TZDIR` / `LOCPATH`. Directly addresses the "host /etc bleedthrough" design decision for the common TLS-sensitive case. Low-risk post-MVP enhancement. See `docs/features/binary_shimming.md` §8 and `DESIGN.md` §8.
- **Static binary fast-path.** If `ENTRYPOINT[0]` is a static ELF with zero `DT_NEEDED` entries, we don't need to unpack the whole rootfs — just extract the binary. Real disk win for Chainguard's static images (`distroless`, `wolfi-base`, etc.). Skipped because the general rootfs-unpack path subsumes it. See `docs/features/binary_shimming.md` §8.

## Disk usage

- **Layer deduplication across packages.** Two packages sharing a Wolfi base each unpack the shared layers independently. A content-addressed layer store with hard-linked files across package dirs would cut disk usage significantly on hosts with many installs. Not MVP because the simple "one directory per package" layout is easier to reason about. See `docs/features/binary_shimming.md` §8.
- **Pruning old digest directories.** After updates, we retain one previous digest dir to enable rollback (see below). Beyond that, nothing prunes. A retention policy (keep N most recent, auto-prune on `dz update --all`) is the natural follow-up. See `docs/features/update_flow.md` §8.

## UX that's cheap and missing

- **`dz rollback <name>`.** With the digest-as-directory layout, rollback is literally a symlink flip (`packages/<name>/current` → previous digest dir). Trivially implementable once `dz update` exists. Worth shipping early because CVE-patch updates occasionally break things and users will want an undo. See `docs/features/update_flow.md` §8.
- **`dz doctor`.** Reconciles drift: wrappers without packages, packages without wrappers, broken `current` symlinks, orphan digest directories, `~/.dropzone/bin` not on `PATH`. Small code surface, high user-confidence payoff. See `docs/features/cli_foundations.md` §6 and `docs/features/host_integration.md` §7.
- **`dz path` helper.** Prints the `PATH` export snippet for users on fish, nushell, tcsh, and anything else we don't auto-configure. One-liner command. See `docs/features/host_integration.md` §7.
- **`dz purge`.** Wipe `~/.dropzone/` entirely — useful for uninstalling dropzone itself without leftovers. See `docs/features/listing_and_removal.md` §7.
- **`dz list --json`.** Scriptable output. See `docs/features/listing_and_removal.md` §7.
- **Shell completion (bash / zsh / fish).** See `docs/features/cli_foundations.md` §6.

## Trust model expansions

- **Key-based cosign signatures.** Some registries (and some private workflows) sign with a keypair rather than Sigstore keyless identity. Policy schema needs a `public_key:` field; verifier path skips Fulcio/Rekor. See `docs/features/attestation_and_verification.md` §9.
- **Private Sigstore instances.** Enterprises run their own Fulcio + Rekor. `sigstore-go` supports pointing at a custom TUF root; we'd expose that per-registry in config. See `docs/features/attestation_and_verification.md` §9.
- **Attestation-based install policies.** "Refuse any image with open criticals per the attached vuln scan." Needs a small DSL; opens a real "policy on install" feature surface. See `DESIGN.md` §9 and `docs/features/attestation_and_verification.md` §9.

## Scope expansions

- **`dz publish`.** Build + sign + push a hardened image through dropzone. Closes the producer-side loop for users who want their own private catalogs. Biggest feature on this list. See `DESIGN.md` §9.
- **Multiple binaries per image.** Honor `CMD` or image-level labels to expose more than just `ENTRYPOINT[0]`. Complicates naming and the wrapper layout. See `DESIGN.md` §9.
- **Dependency resolution between packages.** Real package-manager territory; requires a manifest format and a solver. See `DESIGN.md` §9.
- **Windows host support.** Non-trivial: no POSIX wrapper scripts, different binary formats, different PATH conventions. See `DESIGN.md` §3 (non-goals) and §9.

## Update ergonomics

- **Auto-update hooks.** A cron-safe `dz update --all --yes --quiet` exit mode. Tempting and dangerous — auto-rolling production tools without eyes on them is how you find out `latest` just got a breaking change. Include with prominent warning, or punt. See `docs/features/update_flow.md` §8.
- **`--notify-only` mode.** Just produces the "X updates available" summary for shell prompts / status lines. Trivial post-MVP. See `docs/features/update_flow.md` §8.
- **Moving-tag reinstall → update routing.** If `dz install jq:latest` is re-run and the digest changed, today we treat it as a plain reinstall. Could detect and route through the update flow for consistency. See `docs/features/install_flow.md` §8.

## How to use this file

Add items here when we *decide not to do something now* but *want to do it later*. Each entry should have enough context that someone (you, future you, a contributor) can pick it up without re-deriving why it matters. Remove items when they ship.

Open questions — "should we do X or Y?" — stay in the relevant feature doc. This file is for things we've committed to revisiting.
