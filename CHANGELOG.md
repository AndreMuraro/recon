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
- `recon version` and `recon help`.

### Changed
- Floats compared exactly are encoded with 17 significant digits in scientific notation
  (`%.16e`), a format engines can reproduce; shortest-digit printing differs between
  them.

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
