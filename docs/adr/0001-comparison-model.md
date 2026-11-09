# ADR 0001: Compare inside each warehouse, row by row only where checksums disagree

**Status:** accepted · **Date:** 2026-11-06

## Context

The job is to prove that a rewritten or migrated table matches the original. A typical
case is a stored procedure in Snowflake replaced by a Databricks job, or a legacy SQL
view replaced by Python. The two tables usually live in different engines and can hold
billions of rows, so neither can be copied to the other side or into memory.

Datafold's open-source `data-diff` solved this by checksumming key ranges in each
database and recursing into the ranges that differ. It was archived in May 2024. What is
left is a small Python fork, commercial services, and in Go a tool that loads both sides
into Arrow and compares them in memory. Rule-based tools (dbt tests, Great Expectations,
Soda) check each table against expectations. They don't check two tables against each
other.

Three things make cross-engine comparison hard, beyond moving the data:

- The same value prints differently in each engine: floats, decimals, timestamps and
  time zones, booleans, NULLs.
- Rewrites legitimately change floating-point results in the last digits, so exact
  equality reports noise.
- Each side can change while the comparison runs.

## Decision

### 1. Checksum bisection, pushed down

Both tables are split into segments by key range. For each segment, each engine returns
`count(*)` and the sum of a 60-bit hash of every row. Segments that match are done. A
segment that differs is split into `b` sub-segments (default 16) and checked again. Once
a segment holds fewer than `t` rows (default 10,000), its rows are fetched from both
sides and compared in Go.

A summed hash is order-independent, so no engine has to sort. A sum of 60-bit values in
`DECIMAL(38,0)` (about 2^126) cannot overflow below about 2^66 rows. The approach costs
O(d · log_b n) round trips for d differing rows, so a table that matches costs one query
per side.

The 60-bit hash is the first 15 hex digits of `MD5` of the row's canonical encoding (see
below). Every target engine has MD5 and a way to read hex as an integer: DuckDB
`('0x' || substr(md5(r), 1, 15))::BIGINT`, Postgres `('x' || ...)::bit(60)::bigint`,
Snowflake `TO_NUMBER(..., 'XXXXXXXXXXXXXXX')` and Databricks `conv(..., 16, 10)`. MD5 is
not a security control here, only a well-spread hash that every engine has. A difference
goes unnoticed only if the segment's counts match and its sums collide, which has
probability about 2^-60 per differing segment. The report states this bound. Anyone who
needs certainty can run `--exhaustive`, which compares every row.

### 2. One canonical encoding, compiled to SQL per dialect

Values are hashed as canonical text that Go defines once. Each dialect has its own SQL
that produces the same bytes:

| Type | Canonical text |
|---|---|
| Integer | Base-10 digits |
| Decimal | Fixed scale, taken from the wider side's declared scale; no exponent |
| Float | Tolerance bucket (section 3), or, when compared exactly, the binary significand and exponent, `<m>p<q>` with x = m·2^q (ADR 0009). Decimal formats can't be reproduced across engines: C and Java round differently |
| Date | `YYYY-MM-DD` |
| Timestamp | Microseconds since the Unix epoch in UTC; naive timestamps are read as UTC unless `--tz` says otherwise |
| Boolean | `t` / `f` |
| String | The raw bytes; NFC normalisation and trimming are opt-in per column |
| NULL | A token no other value can produce (`\N`), with an `\`-escape for strings |

Columns are joined with a separator that can't occur unescaped, and they're joined in
the order given by the column mapping, never by position.

Each dialect's encoder is tested against the Go reference encoder on generated values.
That test, not this table, defines correctness.

### 3. Tolerance without false negatives

Checksums need exact equality, while tolerances are about closeness. The two are
reconciled by hashing a **bucket** of each tolerant value instead of the value itself:

- **Absolute tolerance ε:** hash `floor(x / w)` with w = ε/2. Values at or beyond 2^52
  widths are hashed exactly instead; this is checked before dividing, so huge values
  never overflow (ADR 0003). If two values land in the
  same bucket, then |a − b| < w < ε. Turned around: any pair farther apart than ε lands
  in different buckets, so its segment is flagged. Within-tolerance pairs that straddle
  a bucket edge cost one extra row-level check and are then reported as equal. Halving ε
  leaves margin for each engine's floating-point error in the division.
- **Relative tolerance r:** hash the sign and `floor(ln|x| / ln(1 + r/2))`, with zero as
  its own bucket. The same argument holds on the log scale.
- **Both:** a pair fails only if it's beyond both tolerances, so beyond ε in particular.
  The absolute bucket alone therefore guarantees no false negatives.

The natural log is taken by `tolerance.LogAbs`, which works around Go's wrong
`math.Log` on subnormal numbers (ADR 0003).

The row-level pass applies the exact tolerance in Go. The checksum stage can only add
work; it can never hide a difference.

### 4. NULLs, NaN and keys

- NULL equals NULL and nothing else, whatever the tolerance. NaN equals NaN. ±Inf are
  equal only to the same infinity.
- A key is required: one orderable column, or a composite key that is segmented on its
  leading column. Before diffing, both sides are checked for duplicate keys. Duplicates
  are reported in their own class rather than silently dropped.

### 5. Consistent snapshots

Comparing two moving tables gives noise. When an engine supports time travel (Snowflake
`AT`, Delta `TIMESTAMP AS OF`), `--as-of` pins each side to a point in time. Otherwise
the report states that the sides were not pinned, and a second pass over the differing
segments shows which differences persisted.

### 6. Output that a CI job can act on

The output is a JSON report (stable schema, versioned) plus an HTML page. Differences
fall into four classes: missing on the left, missing on the right, value differences
(naming the columns), and duplicate keys. The exit code is 0 when the tables match, 1
when they differ, and 2 on errors. A Go library API for use inside tests comes
from the same core.

## Consequences

- Each dialect needs an encoder, plus a conformance test against the reference encoder.
  Adding an engine means writing both, not just a driver.
- Postgres, Snowflake and Databricks have pure-Go `database/sql` drivers, so the default
  binary stays static and cross-compiles. DuckDB's driver needs cgo, so DuckDB sits
  behind a build tag and ships in per-platform release builds. It is the engine for
  local files (Parquet, CSV) and for most tests.
- The design fits key-based tables. Tables without a key, or with free-form duplicates,
  are out of scope for v0.1. A multiset comparison of whole-row hashes could cover them
  later.
- Bucketing means a float column compared with a tolerance can't also be checked for
  exact equality in the same run. That is the intended trade-off.
