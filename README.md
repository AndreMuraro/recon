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

> **Status: early development.** The canonical encoder and tolerance buckets are built and fuzz-tested. The `diff` command shown above is the design target ([ADR 0001](docs/adr/0001-comparison-model.md)) and doesn't exist yet.

## How it works

1. Both tables are split into key ranges. Each warehouse returns a row count and a sum of row hashes for every range, so data stays where it is.
2. Ranges whose checksums disagree are split again, down to small ranges that are fetched and compared row by row.
3. Values are hashed in one canonical form that each SQL dialect reproduces exactly, so `1.50` in Snowflake and `1.5` in Databricks hash the same, and so do a UTC and a naive timestamp for the same instant.
4. Columns with a tolerance are hashed by **bucket**, chosen so that any pair outside the tolerance always lands in different buckets. A difference can't hide in a checksum.

## Engines

| Engine | Driver | Static binary | Status |
|---|---|---|---|
| PostgreSQL | pgx | yes | planned |
| DuckDB (and local Parquet/CSV) | duckdb-go (cgo) | per-platform builds | planned |
| Snowflake | gosnowflake | yes | planned |
| Databricks | databricks-sql-go | yes | planned |

## Decisions

- **Compare in place.** Checksums are computed in each warehouse; only differing rows leave it. [ADR 0001](docs/adr/0001-comparison-model.md)
- **Tolerance without false negatives.** Values are bucketed at half the tolerance width, which is proven, and fuzz-tested in CI, to never hide a difference. [ADR 0001 §3](docs/adr/0001-comparison-model.md)
- **One reference encoder.** Each dialect's SQL encoder is tested against the Go encoder; golden values come from an independent implementation.
- **Standard Go, strictly checked.** The standard library first, two supported Go releases, race and fuzz tests, govulncheck, and static, reproducible, attested releases. [ADR 0002](docs/adr/0002-go-toolchain-and-layout.md)

## Why this exists

Datafold's open-source `data-diff` did this, and it was archived in May 2024. Rule-based tools (dbt tests, Great Expectations, Soda) check a table against expectations, not against another table.

## Development

```console
go test -race ./...     # tests, with the race detector
golangci-lint run       # lint and format checks (v2.15+)
go test -run='^$' -fuzz=FuzzNoFalseNegatives ./internal/tolerance   # fuzz one target
```

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT
