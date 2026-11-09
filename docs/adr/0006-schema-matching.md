# ADR 0006: Reading and matching schemas

**Status:** accepted · **Date:** 2026-11-09

## Context

Until now a comparison started from a schema written in Go: column names, canonical
types and scales. A person comparing two tables shouldn't have to write that. recon has to
read each table's columns from its engine and decide how each pair is compared. Rewrites
change schemas in small ways that must not get in the way: Snowflake upper-cases unquoted
names where Postgres lower-cases them, an `INTEGER` becomes a `NUMBER(38, 0)`, a
`DECIMAL(10, 2)` becomes a `numeric(12, 3)`. Other differences must stop the comparison,
because comparing them would mean guessing.

## Decision

**Columns come from `information_schema.columns`**, which every target engine has: name,
data type, numeric precision and scale, and position. Like the segment queries, the query
returns text only and has no ORDER BY (ADR 0004). Table names are matched as stored, and
written as string literals by each dialect (`E'…'` in Postgres, so the meaning doesn't
depend on `standard_conforming_strings`).

**Each dialect maps its types to canonical ones, and refuses what it can't compare
exactly:**

| Canonical | DuckDB | Postgres |
|---|---|---|
| integer | `TINYINT`…`BIGINT`, `UTINYINT`…`UINTEGER` | `smallint`, `integer`, `bigint` |
| decimal | `DECIMAL(p, s)`, p ≤ 38 | `numeric(p, s)`, p ≤ 38 |
| float | `FLOAT`, `DOUBLE` | `real`, `double precision` |
| boolean, date | `BOOLEAN`, `DATE` | `boolean`, `date` |
| timestamp | `TIMESTAMP`, `TIMESTAMPTZ`, `TIMESTAMP_S`, `TIMESTAMP_MS` | `timestamp`, `timestamptz` |
| string | `VARCHAR` | `text`, `varchar`, `char` |

Refused, each with the reason: `UBIGINT` and `HUGEINT` (values may not fit in 64 bits),
`TIMESTAMP_NS` (nanoseconds would be truncated), `numeric` without a declared scale or
above 38 digits, and anything else (UUID, JSON, arrays, binary, time of day). A refused
column is an error only if it is compared, so `--exclude` lets the rest be compared.

**Columns pair by name, ignoring case.** The left table's spelling is used in reports. A
table with two columns whose names differ only in case is refused, unless they're excluded.

**A pair is compared if both sides have the same canonical type,** or one is an integer
and the other a decimal, compared as the decimal. Decimals are compared at the larger of
the two scales, so values are padded, never rounded. Anything else, such as a float
against a decimal, is refused: the person can compare views that cast one side, and the
cast is then visible in their own SQL.

**The key must be an integer on both sides** (ADR 0004), and tolerances apply only to
columns that are floats on both sides.

**Every column must be on both sides or excluded.** A column on one side only is a
difference in the schema, and recon doesn't decide whether it matters.

**All problems are reported at once,** each naming the column, both types and what to do,
so a person fixes the comparison in one pass rather than one error per run.

## Consequences

- Most comparisons need only the two table names and the key.
- `char(n)` is compared as Postgres reads it as text: without padding. A `VARCHAR` with
  trailing spaces on the other side will differ, which is the honest result.
- Types are refused rather than approximated. Supporting more (UUID, time of day) means
  adding them to the canonical encoding and to conformance, not a mapping here.
