package packagehandler

// Real package handler behavior is tested via:
//   - internal/registry    (pull + platform selection)
//   - internal/shim        (entrypoint, loader, wrapper, build)
//   - internal/localstore  (metadata and current-symlink management)
//   - internal/hostintegration (wrapper install/remove, PATH)
//
// The orchestration this package performs is a thin chain across those.
// A full end-to-end install against an in-process registry lives in
// internal/app as the CLI-level integration test so it exercises the
// command wiring at the same time.
