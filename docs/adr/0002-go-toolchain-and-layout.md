# ADR 0002: Go toolchain, layout and checks

**Status:** accepted · **Date:** 2026-11-06

## Context

recon is a command-line tool and a library. It talks to several warehouses at once and
runs in CI jobs and migration pipelines. It should be quick to install, behave the same
on every platform, and prove its own correctness. It's also its author's first Go
project, so the conventions here favour what the Go team and the wider community treat
as standard over anything clever.

## Decision

**Language and support window.** Go, supporting the two most recent releases (1.26 and
1.27 today). That is the window the Go team ships security fixes for. `go.mod` declares
the oldest supported version, and CI tests both.

**Layout.**

```
cmd/recon/          the CLI: argument parsing and output only
internal/canon/     canonical row encoding and checksums (the reference encoder)
internal/tolerance/ float equality and tolerance buckets
docs/adr/           decisions
```

- Everything that isn't a stable public API lives under `internal/`, which Go refuses to
  let other modules import. A public library package comes once the API has settled;
  until then, nothing is promised that could break callers.
- No `pkg/` directory. The Go project's module layout guidance doesn't use one, and it
  adds a path segment without adding meaning.
- `main` holds no logic beyond `run(args, stdout, stderr) int`, so the whole command line
  is tested in-process.

**Dependencies.** The standard library first. The CLI uses `flag` with one flag set per
command, which is enough for a few commands and adds nothing to the supply chain. Each
warehouse driver is a deliberate addition with its own justification in the change that
adds it. Dependabot proposes updates weekly.

**Style.**
- Errors are values: sentinels named `ErrX` for callers to test with `errors.Is`,
  wrapped with `%w` and context. Panics only for programmer errors that cannot happen.
- Functions take `context.Context` first wherever they do I/O.
- Append-style encoders (`AppendRow(dst, row)`) instead of returning fresh slices, as
  in `strconv`, so that hot loops don't allocate.

**Tests.**
- Standard `testing` only: table-driven subtests and `t.Parallel()` everywhere. CI runs
  them with the race detector and `-shuffle=on`.
- **Native fuzzing** for every invariant the comparison depends on: tolerance buckets
  never hide a difference beyond tolerance, the row encoding is injective, and decimal
  text is canonical. CI runs each fuzz target for 30 seconds on every change.
- Golden values come from an independent implementation (Python's `hashlib`), so an
  encoder bug can't also be baked into its own expected output.

**Checks in CI.** Every job has read-only permissions, and Actions are pinned to commit
SHAs.
- golangci-lint v2 with a strict linter set and gofumpt formatting.
- `go mod tidy -diff`, plus a check that re-derives `go.sum` through the Go checksum
  database.
- Tests on Linux with both Go releases, and on macOS and Windows with the latest.
- Fuzzing, as above.
- `govulncheck`, which reports only vulnerabilities in code the program can actually
  reach.
- zizmor for the workflows themselves.
- A GoReleaser snapshot build of every platform.

**Releases.** GoReleaser builds static binaries (`CGO_ENABLED=0`, `-trimpath`, commit
timestamps) for Linux, macOS and Windows on amd64 and arm64, with checksums and an SBOM
per archive. The release workflow attests build provenance for every archive through
GitHub's attestation service, so anyone can verify a download with `gh attestation
verify`. Tags are pushed by a person, never by automation.

## Consequences

- DuckDB's driver needs cgo, which conflicts with static builds. It will come with a
  build tag and a separate per-platform release (ADR 0001).
- `flag` gives no shell completion or nested help. If the CLI grows past a few commands,
  that will be revisited in a new ADR, not slipped in.
- Linting needs Go 1.26 or later, because golangci-lint v2.15 is built with it.
