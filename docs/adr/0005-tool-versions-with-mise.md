# ADR 0005: Tool versions with mise

**Status:** accepted · **Date:** 2026-11-07

## Context

recon's checks use six tools besides Go itself: golangci-lint, govulncheck, GoReleaser,
zizmor and the DuckDB CLI. Their versions were pinned in four places: environment
variables in each workflow, a hash-pinned pip requirements file for DuckDB, and, for a
contributor, whatever they had installed. A contributor could pass `golangci-lint run`
locally on one version and fail CI on another.

## Decision

- **`mise.toml` is the one place tool versions are pinned,** exact to the patch.
  [mise](https://mise.jdx.dev) installs them on Linux, macOS and Windows; contributors run
  `mise install`, and `mise run check` runs what CI runs.
- **CI installs the same file** with `jdx/mise-action`, pinned to a commit SHA and to a
  mise version (`min_version` in `mise.toml` matches it). Jobs install only the tools they
  use. Jobs that build release binaries, and the release workflow, don't use a cache.
- **`mise.lock` records each tool's download URL and checksum** for Linux, macOS and
  Windows. mise-action installs with `--locked` when it exists, so a tool whose download
  changes fails the job. That replaces the pip hash pins the DuckDB CLI had.
- **The test matrix keeps `actions/setup-go`.** It tests both supported Go releases at
  their latest patch (ADR 0002), which is a different job from installing one pinned
  toolchain.
- **Updates are deliberate.** Dependabot doesn't read `mise.toml`. `mise outdated` lists
  newer releases, and `mise upgrade --bump` updates the pins, followed by `mise lock`.
  Dependabot still updates Go modules and Actions, mise-action included.

## Consequences

- One edit changes a tool everywhere, and local results match CI's.
- Tool updates no longer arrive as pull requests; they need someone to run
  `mise outdated`. Renovate reads `mise.toml` and could automate that later.
- mise itself joins the supply chain. It verifies its own releases, it is pinned in CI,
  and the lockfile pins what it downloads.
