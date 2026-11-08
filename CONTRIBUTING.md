# Contributing

## Setup

Install [mise](https://mise.jdx.dev/getting-started.html), then run `mise install` in the
repository. It installs the exact versions in `mise.toml`, which CI uses too: Go,
golangci-lint, govulncheck, GoReleaser, zizmor and the DuckDB CLI. To change a version,
edit `mise.toml` and run `mise lock`, and commit both files.

On Windows, the race detector needs a C compiler (cgo). Without one, run `go test ./...`
and let CI run the race tests.

## Before opening a pull request

```console
mise run check
```

That runs the linters and `go mod tidy -diff`, the tests with the race detector,
govulncheck, zizmor on the workflows, and the DuckDB engine tests.

If you change an invariant (encoding, buckets, comparison), run its fuzz target for a few
minutes locally; CI runs each one for 30 seconds:

```console
go test -run='^$' -fuzz=FuzzStringRowsRoundTrip -fuzztime=5m ./internal/canon
```

A failing fuzz input is saved under `testdata/fuzz/`. Commit it with the fix, so it stays
a regression test.

## Decisions

Anything that changes behaviour across engines, adds a dependency, or changes the public
surface gets an ADR in `docs/adr/`, numbered in sequence.
