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
- `recon version` and `recon help`.
- CI: golangci-lint v2, tests on Go 1.26 and 1.27 with the race detector, fuzzing,
  govulncheck, zizmor and GoReleaser snapshot builds. A release workflow with
  provenance attestations.
