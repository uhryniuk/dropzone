# Dropzone

Dropzone is a CLI that installs binaries from signed OCI container images directly onto your Linux or macOS host. It treats container registries as package registries: add a registry, browse it, install the entrypoint binary, and keep it up to date as the source registry rebuilds the same tag. Every install verifies a cosign signature against a per-registry identity policy, so you have a cryptographic record of who built the binary you're running.

No container runtime is required at use time. The extracted binary runs natively against its bundled libraries.

## Concept

Dropzone is the consumer side of signed OCI images: it pulls the image, verifies the signature against a registry-scoped identity policy, and runs the entrypoint binary natively on your host.

1. Pull a signed OCI image from any registry. The default is `cgr.dev/chainguard`.
2. Verify the image signature with cosign against a per-registry identity policy. Fail closed unless `--allow-unsigned` is passed and the registry has no policy.
3. Unpack the rootfs into `~/.dropzone/packages/<name>/<digest>/rootfs/` and write a wrapper script at `~/.dropzone/bin/<name>`.
4. `dz update` queries the registry for digest drift on the installed tag (rebuilds of the same tag) and for newer tags.

What dropzone gives you is a verified provenance trail: the binary at `~/.dropzone/bin/jq` came from this image, signed by this identity, attested to by this SBOM. What it does not do is judge image content. "Was this image built minimally?" or "Are there CVEs in here?" are questions for the publisher and the attached vulnerability-scan attestation, which dropzone surfaces but does not gate on.

`dz install jq` against the default Chainguard registry verifies Chainguard's GitHub Actions signing identity and prints the SBOM and provenance summary. Any OCI registry works; unsigned images need `--allow-unsigned`.

## Features

* `dz install <ref> [--allow-unsigned]` and `dz remove <name>` for the lifecycle.
* `dz add registry`, `dz list registries`, `dz remove registry`, `dz search`, `dz tags` for managing sources.
* `dz update [<name>] [--check] [--all] [--yes]` detects same-tag digest drift and newer tags, applies updates atomically.
* `dz rollback <name>` flips the package back to its previous digest directory.
* `dz doctor [--fix]` reports orphan wrappers, broken symlinks, packages without wrappers, and PATH issues.
* `dz path setup` and `dz path unset` manage the shell rc edit. Install never touches shell rc files on its own.
* `dz login` and `dz logout` for private registries. Docker credentials work too via the chained keychain.
* `dz purge` wipes `~/.dropzone/` after confirmation.
* `dz list --json` for scripting. `dz completion {bash|zsh|fish|powershell}` for shell completion.

## Installation

### Prerequisites

* Linux (x86_64 or aarch64) or macOS (x86_64 or arm64).
* A POSIX shell at `/bin/sh` for the per-package wrapper scripts.
* Go 1.23 or newer if building from source.

There are no runtime dependencies. Dropzone ships as a single Go binary. Cosign and patchelf are not required on your host. Signature verification uses `sigstore-go` as an embedded library, the OCI registry client is `go-containerregistry`, and binary integration is a tar unpack plus a wrapper script. No binary rewriting.

### Platform note

Dropzone installs whatever platform entry the registry resolves for your host. If an image ships only `linux/*` platforms, it will not install on macOS. Chainguard's current public catalog is Linux-only.

### Build from source

```bash
git clone https://github.com/uhryniuk/dropzone.git
cd dropzone
CGO_ENABLED=0 go build -o dz ./cmd/dropzone
sudo mv dz /usr/local/bin/
```

`CGO_ENABLED=0` produces a fully static binary on Linux. On macOS the result links only against system-provided frameworks.

On first run, dropzone creates `~/.dropzone/` and seeds the default `chainguard` registry with the right cosign identity policy. It also prints how to add `~/.dropzone/bin` to your `PATH`, which `dz path setup` will do for you (zsh and bash).

## Usage

### Install a package

```bash
dz install jq                                 # short name, expanded against the default registry
dz install chainguard/jq:1.7.1                # registry name + image + tag
dz install mycorp/internal-tool --allow-unsigned   # registry without a configured policy
```

### Manage registries

```bash
dz list registries

dz add registry mycorp registry.mycorp.example/signed \
  --template github \
  --identity-regex 'https://github.com/mycorp/.*'

dz remove registry mycorp
```

The `--template` flag pre-fills the OIDC issuer for `github`, `gitlab`, `google`, and `chainguard`. The chainguard template ships fully formed; the others need `--identity-regex`. `--default` also flips the default registry to the one being added.

### Browse

```bash
dz search                          # list images in the default registry's catalog (when /v2/_catalog is exposed)
dz search openssl --registry chainguard
dz tags jq                         # list tags for an image
```

Many registries (Docker Hub, GHCR) disable the catalog endpoint. In those cases `dz search` says so and `dz tags <image>` is the fallback.

### List, update, remove, rollback

```bash
dz list                  # installed packages
dz list --json           # machine-readable

dz update                # status across all installed packages
dz update jq             # check + prompt for one
dz update --all --yes    # apply all available updates without prompting

dz remove jq
dz rollback jq           # flip back to the previous digest after a bad update
```

### Authenticate to a private registry

```bash
dz login registry.mycorp.example -u alice
echo "$TOKEN" | dz login ghcr.io -u alice --password-stdin

dz logout registry.mycorp.example
```

Credentials are stored at `~/.dropzone/auth.json` (mode 0600) in the same format as Docker's `config.json`. The keychain falls through to Docker's config and credential helpers, so `docker login` continues to work.

### PATH integration

```bash
dz path                  # status: bin dir, on-PATH, shell, rc file, rc block presence
dz path setup            # append a marked block to ~/.zshrc or ~/.bash_profile / ~/.bashrc
dz path unset            # remove the marked block
```

### Diagnostics

```bash
dz doctor                # report drift between expected and on-disk state
dz doctor --fix          # apply safe automatic remediations
```

## How install works

1. Resolve the reference to a registry, image, and tag, then to a concrete digest. Pick the manifest entry that matches your host OS and arch.
2. Verify the image signature with `sigstore-go` against the registry's policy. Fail closed unless `--allow-unsigned` is passed and the registry has no policy.
3. If verified, fetch any in-toto attestations attached to the image and parse SBOM, SLSA provenance, and vulnerability-scan summaries.
4. Unpack the image's full rootfs into `~/.dropzone/packages/<name>/<digest>/rootfs/`. The container's libraries, dynamic loader, and bundled resources all come along.
5. Generate a POSIX wrapper script at `~/.dropzone/bin/<name>` that invokes the entrypoint from the bundled rootfs with library-search environment variables pointing at the rootfs's `lib` directories. The original binary is not modified.
6. Record the resolved digest in package metadata so `dz update` can detect upstream rebuilds of the same tag.

## Where things live

```
~/.dropzone/
├── bin/                          per-package wrapper scripts
├── packages/
│   └── <name>/
│       ├── current               symlink to the active digest dir
│       └── <digest>/
│           ├── rootfs/           full unpacked image
│           └── metadata.json
├── cache/                        registry catalog/tag cache
├── config/
│   └── config.yaml
└── auth.json                     private-registry credentials (0600)
```

## Status

v0.1 is feature-complete. See `docs/roadmap.md` for the per-phase build history and `BACKBURNER.md` for the deferred items (per-attestation cryptographic verification, attestation-based install policies, semver-aware tag ordering, layer deduplication, `dz publish`, Windows host support, and a few smaller polish items).

## License

[MIT](LICENSE)
