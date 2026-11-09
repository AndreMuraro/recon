# recon

[![CI](https://github.com/AndreMuraro/recon/actions/workflows/ci.yml/badge.svg)](https://github.com/AndreMuraro/recon/actions/workflows/ci.yml)

**Prove that a rewritten table matches the original, across warehouses, without moving the data.**

You rewrote a Snowflake stored procedure as a Databricks job, or a legacy SQL view as Python. Before switching over, you need evidence that every row, across the full history, comes out the same, within the floating-point noise a rewrite legitimately introduces. recon compares the two tables where they live and reports exactly which rows and columns differ.

```console
$ recon diff \
    --left  'snowflake://…/ANALYTICS/FINANCE/VOYAGE_PNL' \
    --right 'databricks://…/main/finance/voyage_pnl' \
    --key voyage_id \
    --tolerance tce_usd_per_day=abs:1e-6 \
    --report report.html
# status: differs · 120,000,000 rows · 3 value differences in 2 columns · 41 queries · 38 s
# exit code 1
```

> **Status: early development.** The canonical encoder, tolerance buckets, the DuckDB and PostgreSQL dialects, and the bisection search are built and tested against the real engines, including a DuckDB table diffed against a Postgres one. The `diff` command shown above is the design target ([ADR 0001](docs/adr/0001-comparison-model.md)): it still needs database drivers and the CLI.

## How it works

1. Both tables are split into key ranges. Each warehouse returns a row count and a sum of row hashes for every range, so data stays where it is.
2. Ranges whose checksums disagree are split again, down to small ranges that are fetched and compared row by row.
3. Values are hashed in one canonical form that each SQL dialect reproduces exactly, so `1.50` in Snowflake and `1.5` in Databricks hash the same, and so do a UTC and a naive timestamp for the same instant.
4. Columns with a tolerance are hashed by **bucket**, chosen so that any pair outside the tolerance always lands in different buckets. A difference can't hide in a checksum.

## Engines

| Engine | Driver | Static binary | Status |
|---|---|---|---|
| PostgreSQL 14+ | pgx | yes | SQL dialect conforms (15–18 in CI); driver planned |
| DuckDB (and local Parquet/CSV) | duckdb-go (cgo) | per-platform builds | SQL dialect conforms; driver planned |
| Snowflake | gosnowflake | yes | planned |
| Databricks | databricks-sql-go | yes | planned |

## Decisions

- **Compare in place.** Checksums are computed in each warehouse; only differing rows leave it. [ADR 0001](docs/adr/0001-comparison-model.md)
- **Tolerance without false negatives.** Values are bucketed at half the tolerance width, which is proven, and fuzz-tested in CI, to never hide a difference. [ADR 0001 §3](docs/adr/0001-comparison-model.md)
- **One reference encoder, checked on real engines.** Each dialect's SQL runs on the engine itself, over thousands of generated rows and edge cases, and must match the Go encoder byte for byte; golden values come from an independent implementation. This found a Go standard-library bug on its first run ([ADR 0003](docs/adr/0003-conformance-against-real-engines.md)).
- **Bisection pushed into the engines.** Disagreeing key ranges are split 16 ways per level, both sides queried in parallel, and rows fetched only for ranges of at most 1,000 rows: in tests, 3 changed rows in 100,000 are found by fetching 148. [ADR 0004](docs/adr/0004-segments-and-bisection.md)
- **Schemas read, not written.** Columns come from each engine's `information_schema`, pair by name across case conventions, and compare an integer against a decimal or two decimal scales without rounding. Anything recon can't compare exactly is refused with the column, both types and what to do. [ADR 0006](docs/adr/0006-schema-matching.md)
- **Standard Go, strictly checked.** The standard library first, two supported Go releases, race and fuzz tests, govulncheck, and static, reproducible, attested releases. Tool versions are pinned once, in `mise.toml`, for contributors and CI alike. [ADR 0002](docs/adr/0002-go-toolchain-and-layout.md), [ADR 0005](docs/adr/0005-tool-versions-with-mise.md)

## Why this exists

Datafold's open-source `data-diff` did this, and it was archived in May 2024. Rule-based tools (dbt tests, Great Expectations, Soda) check a table against expectations, not against another table.

## Development

Tool versions are pinned in [`mise.toml`](mise.toml) and installed by [mise](https://mise.jdx.dev), locally and in CI:

```console
mise install          # Go, golangci-lint, govulncheck, GoReleaser, zizmor and the DuckDB CLI
mise run check        # what CI checks: lint, tests, govulncheck, zizmor, engine tests
mise run test         # tests, with the race detector
mise run engines      # conformance and diff tests on DuckDB

# Postgres too: point it at a server you can reach with psql
RECON_POSTGRES="psql postgresql://postgres@localhost/postgres" mise run engines

go test -run='^$' -fuzz=FuzzNoFalseNegatives ./internal/tolerance   # fuzz one target
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT
