# Contributing

## Setup

- Go 1.26 or later. With `GOTOOLCHAIN=auto` (the default), Go fetches the right version.
- [golangci-lint](https://golangci-lint.run/welcome/install/) v2.15 or later.

## Before opening a pull request

```console
go test -race -shuffle=on ./...
golangci-lint run
go mod tidy -diff
```

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
