# ADR 0010: Snowflake and Databricks

**Status:** accepted · **Date:** 2026-11-09

## Context

recon exists for the rewrite this README opens with: a Snowflake procedure rebuilt as a
Databricks job. Both engines are services. Neither runs in CI the way DuckDB and Postgres
do (ADR 0003), and their SQL differs from both in the places the canonical encoding
depends on: string escapes, hashing, exact sums, timestamps and float formatting. The
last needed a change of encoding first (ADR 0009).

## Decision

### The dialects

| | Snowflake | Databricks (Spark SQL 3.4+) |
|---|---|---|
| Identifiers | `"…"`, case-sensitive when quoted; unquoted names are stored in upper case | `` `…` ``; case-insensitive, stored in lower case |
| String literals | backslash escapes, so every literal goes through `StringLiteral` | the same |
| 60-bit hash | `FLOOR(MD5_NUMBER_UPPER64(text) / 16)`: the first 16 hex digits as an integer, less the last | `CAST(conv(substr(md5(text), 1, 15), 16, 10) AS BIGINT)` |
| Exact sum | `NUMBER(38, 0)` | `DECIMAL(38, 0)` |
| Timestamps | from `DATE_PART(EPOCH_NANOSECOND, …)`, see below | `unix_micros`; `TIMESTAMP_NTZ` read through `ReadColumn`, see below |
| Integers | every scale-0 `NUMBER`, since `INTEGER` is `NUMBER(38, 0)` | `TINYINT` to `BIGINT` |

**Snowflake timestamps can hold nanoseconds; the canonical form holds microseconds.** A
value with a sub-microsecond part is written as `ns` followed by its nanoseconds since the
epoch, which no microsecond value's text can equal. A difference below a microsecond is
reported rather than truncated away. Everything else is microseconds, as for every engine.

**Databricks reads `TIMESTAMP_NTZ` in the session time zone when cast to `TIMESTAMP`.**
Every dialect reads a timestamp without a time zone as UTC, so Databricks reads an NTZ
column through an expression that does exactly that:
`timestamp_micros(timestampdiff(MICROSECOND, TIMESTAMP_NTZ '1970-01-01 00:00:00', col))`.
This is the first column type whose reading depends on its declared type, so dialects
gain an optional `ColumnReader`, and schema matching reads columns through it.

**Each catalog has its own `information_schema`** in both engines, so a three-part table
name is looked up in `catalog.information_schema.columns`.

**Databricks SQL works with ANSI mode on or off:** nothing it computes can overflow or
divide by zero.

### Conformance without the engines

Each dialect's conformance runs as a **generated script**: one `SELECT` that builds 400
generated rows, the same as the CLI conformance tests use, from the same seed; computes
every value's and row's canonical text with the dialect's SQL; compares them with the Go
reference's texts, embedded as literals; and returns a single row `pass`, or one row per
mismatch. Values near a log-bucket edge are skipped, as in ADR 0003.

- The scripts are committed (`internal/dialect/testdata/conformance`), and a test fails
  when one is out of date, so a change to a dialect or to the encoding can't leave them
  behind. `go test ./internal/dialect -run ConformanceScripts -update` rewrites them.
- **The Databricks script runs in CI on open-source Spark** 3.5 and 4, the engine
  Databricks Runtime is built on, with ANSI mode on and off and the session in a non-UTC
  time zone. PySpark is installed with `pip --require-hashes`.
- **The Snowflake script is run by hand** in a Snowflake worksheet, before a release and
  after any change to the Snowflake dialect. There is no faithful local Snowflake.
- Both scripts stay under Snowflake's 1 MB statement limit.

## Consequences

- Connecting to either engine needs its driver (gosnowflake, databricks-sql-go) and URL
  schemes in `internal/connect`. That is a separate change, with its own justification
  (ADR 0002).
- Snowflake's conformance depends on a person running a script, which CI can't enforce.
  The README and CONTRIBUTING say when to run it.
- Databricks and Spark can drift apart. A Databricks run of the script, by hand, catches
  that; Spark in CI catches everything else on every change.
- Table names are matched as stored: upper case for unquoted Snowflake names, lower case
  for Databricks. Folding unquoted names per engine would be friendlier and is left for
  later.
