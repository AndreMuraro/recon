# Changelog

All notable changes are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- Canonical row encoding with 60-bit MD5 checksums (`internal/canon`), golden-tested
  against Python's `hashlib`, with fuzz tests for injectivity and canonical decimals.
- Float tolerances (absolute, relative or both) and tolerance buckets
  (`internal/tolerance`), fuzz-tested to never hide a difference beyond tolerance.
- DuckDB SQL dialect (`internal/dialect`): canonical text and 60-bit checksums computed
  inside DuckDB, reading each side's columns through a mapping function, conformance-tested on the official DuckDB CLI over 4,000 generated rows
  and edge cases for every type (ADR 0003).
- PostgreSQL SQL dialect (14 or later), conformance-tested on Postgres 15, 16, 17 and
  18 in CI through psql. The conformance harness now runs the same generated rows on any
  engine.
- Checksum bisection and row comparison (`internal/diff`, ADR 0004): tables are compared
  on an integer key by splitting disagreeing key ranges into 16 children per level,
  querying both sides in parallel, and merge-joining rows only in ranges of at most
  1,000 rows. Differences are reported as only-left, only-right or changed (with the
  columns), and tolerant floats are compared exactly against their tolerance. Tested
  against an in-memory model built on the Go reference encoding, and end to end on
  DuckDB, Postgres, and DuckDB against Postgres.
- Search limits: `MaxRowsFetched` (default 100,000) and `MaxDifferences` (default
  10,000). Ranges a limit leaves uncompared are reported as unresolved, with their row
  counts, so a limited result never reads as equal.
- Schema reading and matching (`internal/schema`, ADR 0006): each table's columns are
  read from `information_schema` and mapped to canonical types per dialect, refusing
  types that can't be compared exactly. Columns pair by name regardless of case; an
  integer can be compared against a decimal, and decimals at the larger scale. Every
  problem (a column on one side only, incompatible types, a non-integer key, a misplaced
  tolerance) is reported at once. Tested on DuckDB and Postgres, including a diff
  between tables whose schemas differ the way rewrites do.
- `diff.DB`, a `Querier` over any `database/sql` driver, scanning every column as text.
- Reports (`internal/report`): a text summary for people and a versioned JSON document
  for programs, with the verdict (`equal`, `differs` or `incomplete`), counts by kind and
  by column, and each difference's values decoded from the canonical encoding.
- `recon diff` (ADR 0007): compares a table on one engine with a table on another,
  given connection URLs, table names and the key, with `--exclude`, `--tolerance`, limits
  and a timeout. Text or JSON reports, to standard output or a file. Exit codes 0 equal,
  1 differs, 3 incomplete, 2 error. Passwords in URLs are redacted from every message.
  Builds without an engine's driver say so.
- The PostgreSQL driver: pgx v5 through `database/sql`, pure Go, so releases stay
  static (ADR 0008). Connection settings follow libpq: `PGPASSWORD`, `.pgpass`, service
  files. Tested end to end on Postgres 15 to 18.
- `recon version` and `recon help`.

### Changed
- Tool versions (Go, golangci-lint, govulncheck, GoReleaser, zizmor, the DuckDB CLI) are
  pinned once in `mise.toml`, installed by mise locally and in CI, with `mise run check`
  for the local equivalent of CI (ADR 0005).
- Floats compared exactly are encoded as their binary significand and exponent,
  `<m>p<q>`, which every engine computes with exact arithmetic (ADR 0009). The earlier
  `%.16e` text can't be produced on Databricks, whose formatter is Java's. Reports show
  floats in shortest decimal form.

### Fixed
- Absolute-tolerance buckets of huge floats divided before checking the limit, which
  overflows in Postgres (an error, not infinity). Values are now compared with
  `Tolerance.AbsLimit()` first.
- Relative-tolerance buckets of subnormal numbers: Go's `math.Log` is wrong for them on
  amd64 (golang/go#56600), so `tolerance.LogAbs` scales them first. Found by the DuckDB
  conformance test.
- CI: golangci-lint v2, tests on Go 1.26 and 1.27 with the race detector, fuzzing,
  govulncheck, zizmor and GoReleaser snapshot builds. A release workflow with
  provenance attestations.
