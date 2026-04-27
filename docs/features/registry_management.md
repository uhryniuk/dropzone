# Feature: Registry Management

## 1. Overview

Registries are first-class. Users add OCI registries (`dz add registry`), list them, browse their catalogs (`dz search`), list tags for a specific image (`dz tags`), and install from them. Every registry has a name, a URL, and an optional cosign policy. No registry-level credential storage, auth is delegated to Docker credential helpers via `go-containerregistry`.

The default `chainguard` registry is pre-seeded on first run with the correct cosign identity policy for Chainguard's build pipeline.

## 2. Goals

*   Talk to OCI registries as first-class package registries, using `/v2/_catalog`, `/v2/<name>/tags/list`, and manifest endpoints directly. No `docker pull` subprocessing.
*   One clean abstraction: a `Registry` is a `{name, url, cosign_policy}`. No type field, no factory, no multi-backend inheritance. All registries speak OCI.
*   Support user-added registries with policy templates (`--template github`, `--template gitlab`) for the common case, plus raw `--identity-issuer` and `--identity-regex` fields for anything else.
*   Degrade gracefully when a registry disables `/v2/_catalog`, which many do.

## 3. Components

### 3.1. `internal/registry/registry.go`

```go
type Registry struct {
    Name         string
    URL          string        // e.g., "docker.io/chainguard" or "registry.example/path"
    CosignPolicy *CosignPolicy // nil means "no policy configured"
}

type CosignPolicy struct {
    Issuer        string
    IdentityRegex string
}
```

### 3.2. `internal/registry/manager.go`

*   **`Manager`**, owns the list of configured registries, loaded from config. Exposes:
    *   `List() []Registry`
    *   `Get(name string) (*Registry, error)`
    *   `Add(r Registry) error`, validate, persist to config.
    *   `Remove(name string) error`, remove from config. Fails if packages installed from this registry are still present (prompts the user to remove them first or pass `--force`).
    *   `Resolve(ref string) (ResolvedRef, error)`, expand a short name (`jq`) against the default registry, or parse a fully qualified ref (`chainguard/jq:3.7` or `docker.io/chainguard/jq:3.7`).

### 3.3. `internal/registry/client.go`

Wraps `google/go-containerregistry` for the `/v2/` operations. All calls use Docker credential helpers for auth; we don't store credentials ourselves.

*   **`Catalog(ctx, r *Registry) ([]string, error)`**, `GET /v2/_catalog`. On `404`, `401`, or `405`, returns a typed `ErrCatalogUnavailable` so the CLI can print a clear message.
*   **`Tags(ctx, r *Registry, image string) ([]string, error)`**, `GET /v2/<image>/tags/list`. Expected to work even when catalog doesn't.
*   **`Digest(ctx, r *Registry, image, tag string) (string, error)`**, resolves a tag to the current digest. Used by `dz update` to detect rebuilds.
*   **`Pull(ctx, r *Registry, ref ResolvedRef, stagingDir string) (*ImageConfig, error)`**, resolves the manifest, picks the host-compatible entry from a manifest list, pulls and flattens layers into `stagingDir`, returns the image config (which includes `Entrypoint`).

### 3.4. `internal/registry/cache.go`

Catalog and tag responses cache under `~/.dropzone/cache/<registry-name>/catalog.json` and `~/.dropzone/cache/<registry-name>/tags/<image>.json`. Short TTL (~1 hour default). `dz update` forces a refresh. Cache miss or stale → live fetch.

### 3.5. Default registry seeding

On first run (triggered by `App.Initialize()`), if `~/.dropzone/config/config.yaml` doesn't exist, write it with:

```yaml
default_registry: chainguard
registries:
  - name: chainguard
    url: docker.io/chainguard
    cosign_policy:
      issuer: https://token.actions.githubusercontent.com
      identity_regex: https://github.com/chainguard-images/images/.*
```

### 3.6. Policy templates

`dz add registry` accepts `--template <name>` as a shortcut for common provider identity pins:

*   `--template github`, `issuer: https://token.actions.githubusercontent.com`. Requires `--identity-regex`.
*   `--template gitlab`, `issuer: https://gitlab.com`. Requires `--identity-regex`.
*   `--template chainguard`, full Chainguard policy. No extra fields required.

Without a template, the user supplies `--identity-issuer` and `--identity-regex` directly. Without either, the registry is added with no policy and every install from it requires `--allow-unsigned`.

## 4. CLI integration

### `dz add registry <name> <url> [flags]`

*   `--template github|gitlab|chainguard`
*   `--identity-issuer <url>`
*   `--identity-regex <regex>`
*   `--default`, also set `default_registry` to this.

Validates that `<name>` is unique and `<url>` is a reachable registry (attempts `GET /v2/`). Persists to config.

### `dz list registries`

Tabular output: NAME, URL, POLICY (`chainguard`, `custom`, or `none`), DEFAULT (asterisk on the default).

### `dz remove registry <name>`

Refuses if packages are still installed from this registry unless `--force`.

### `dz search [<term>] [--registry <name>]`

`--registry` defaults to `default_registry`. Hits `Catalog()`; on `ErrCatalogUnavailable` prints:

```
Registry "mycorp" does not expose /v2/_catalog. Use `dz tags <image>` to list tags for a specific image.
```

`<term>` filters the catalog results by substring match.

### `dz tags <image> [--registry <name>]`

Hits `Tags()`. Expected to work even when `search` doesn't.

## 5. Technical details

*   **Library choice:** `google/go-containerregistry` as a Go library, not a subprocess. One code path across `Catalog`, `Tags`, `Digest`, `Pull`. Swapping to `oras-project/oras-go` post-MVP is an option if we want stronger OCI artifact support.
*   **Reference parsing:** `name.ParseReference` from go-containerregistry handles both short and fully qualified refs. We prepend `default_registry.url + "/"` when the user passes a bare image name.
*   **Host-compatible manifest selection:** On a manifest list, select the entry matching `runtime.GOOS` + `runtime.GOARCH`. Supported combinations: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`. No match → fail with an error that lists the platforms the image actually offered.

## 6. Testing

### 6.1. Unit

*   `Resolve()` with short names, fully qualified refs, refs with and without tag.
*   `Catalog()` with a mock registry returning 200, 404, 401, 405, each maps to the right outcome.
*   `Tags()` with pagination (the distribution spec allows `_link` headers).
*   `Digest()` returns the right sha256 for a tag.
*   Policy template expansion.
*   Cache TTL behavior (hit, stale miss, forced refresh).

### 6.2. Integration

*   Start the `registry:2` distribution image locally, push a test image, verify `Catalog`, `Tags`, `Digest`, `Pull` work end-to-end.
*   Point at `docker.io/chainguard` in a network-enabled test; verify `Tags jq` returns plausible data. (Skip in CI if offline.)
*   `dz add registry` with each template, verify resulting config.
*   `dz search` against a registry with catalog disabled prints the expected fallback message.

## 7. Open questions

*   **Catalog pagination:** large registries paginate `_catalog`. We should follow all pages; need a sane cap so a misconfigured endpoint doesn't blow up.
*   **Wildcard identity regexes in policy templates:** `--template github` without an `--identity-regex` defaults to… nothing? Refusing to add with only a template seems right, a template isn't a policy on its own without the regex.
*   **Registry-level auth beyond Docker config:** some registries use OAuth device flow. Out of MVP; falls back to "configure your Docker credential helper."
