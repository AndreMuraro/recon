# ADR 0003: Test each dialect against the real engine, through its CLI

**Status:** accepted · **Date:** 2026-11-07

## Context

Each SQL dialect reproduces the canonical encoding from ADR 0001 inside its engine. The
encoding is only useful if the two match byte for byte, and only the engine itself can
show that they do. Engines differ in exactly the places that matter:

- DuckDB prints the float 1e5 as `100000.0`, while Go's shortest form is `1e+05`.
- Math libraries round `ln` differently in the last place.
- Go's own `math.Log` is wrong for subnormal numbers on amd64 (see below).

Test fixtures written by hand only show what the author thought of.

## Decision

- **Conformance tests run the dialect's SQL on the engine.** Each test generates thousands
  of rows from a fixed seed: random values plus edge cases for every type. These include
  NaN, the infinities, subnormals, values on and one step either side of bucket edges,
  values past the absolute-bucket limit, the full int64 range, years 1 to 9999, time zone
  offsets, and strings full of separators, escapes, quotes and multi-byte characters. The
  rows go into a typed table on the engine. Each column's canonical text, each row's text
  and each checksum are then compared with the Go reference.
- **Engines are reached through their official command-line clients, not Go drivers.**
  The test sends a SQL script on stdin and reads JSON back. That keeps cgo and the
  drivers' dependency trees out of the test of a pure string generator, and makes the
  test easy to point at any installed engine version.
- **The engine is named by an environment variable** (`RECON_DUCKDB`). Without it the test
  is skipped, so `go test ./...` stays hermetic. The CI `conformance` job always sets it.
- **The CLI is pinned and hash-checked.** DuckDB publishes its official CLI on PyPI as
  `duckdb-cli`. CI installs it with `pip --require-hashes` from
  `.github/requirements/duckdb-cli.txt`, and Dependabot proposes updates.
- **The only tolerated mismatch is in relative buckets,** and only within a few units in
  the last place of a log-bucket edge, computed from the actual values. Libraries may
  round `ln` either way there; per ADR 0001 that costs a row-level check, never a missed
  difference. More than 0.1% of values in that window fails the test.

## What it found

The first run failed on 55 of 4,000 relative-bucket values, all of them 5e-324.

- Go's `math.Log` uses an assembly routine on amd64 that mishandles subnormals: it returns
  −709.09 for 5e-324 instead of −744.44. This is a known, open Go issue,
  [golang/go#56600](https://go.dev/issue/56600) from 2022, and the routine is unchanged in
  Go 1.27.3.
- `tolerance.LogAbs` scales subnormals into the normal range before taking the log, and
  the run then matched on every value.
- An encoding fixed by hand from documentation would have kept the bug.

## Consequences

- Every new dialect comes with a conformance test against its engine, or it isn't
  merged. Postgres can run in a CI service container. Snowflake and Databricks need
  credentials, so they will run on demand or on a schedule rather than on every pull
  request.
- The CLIs are test tools only; recon itself will talk to engines through drivers.
  Conformance establishes what the SQL computes, whichever client sends it.
