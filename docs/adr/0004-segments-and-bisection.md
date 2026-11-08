# ADR 0004: Segments, bisection and row comparison

**Status:** accepted · **Date:** 2026-11-07

## Context

ADR 0001 fixes the comparison model: checksum key ranges inside each engine, split the
ranges that disagree, and compare rows only where the checksums can't decide. This ADR
records how that is built: what a segment is, how ranges are split, what the engines are
asked, and how rows are compared.

## Decision

**Keys.** v0.1 compares tables on a single integer key. Integer keys can be split by
arithmetic with no extra queries, and they cover most fact tables and surrogate keys.
String, date and composite keys need split points sampled from the data. That is a later
ADR, not a special case added here. Both sides are checked for duplicate keys first
(`count(*)` against `count(DISTINCT key)`), and duplicates are an error that names how
many there are. Key-based diffing would otherwise silently pair arbitrary rows.

**The key is part of every row's hash.** Each checksum covers the key and the compared
columns together. Without the key, swapping two rows' values between their keys would
leave a segment's multiset of row hashes, and so its sum, unchanged: a false negative.

**Ranges are inclusive** (`lo <= key <= hi`), so the full int64 range is expressible
without overflow. A range is split into at most `b` children (default 16) of near-equal
width, computed in Go with unsigned arithmetic. The engine assigns rows to children with a
`CASE key < split_1 THEN 0 WHEN key < split_2 THEN 1 ... END`. That is the same SQL on
every engine, with no integer division and no overflow, because the split points are
literals.

**Three queries per engine, all returning text:**

| Query | Returns |
|---|---|
| Bounds | row count, distinct keys, min and max key |
| Checksums of a range's children | per child: index, row count, exact sum of the 60-bit row checksums |
| Rows of a range | each compared column as comparison text, the key first |

Sums are exact: `numeric` in Postgres, `HUGEINT` in DuckDB. Everything comes back as
text, so the engine-facing interface is "run this query, give me strings". No
driver-specific type mapping can creep into the comparison.

**No query has an ORDER BY.** Engines resolve a bare name in ORDER BY against the output
columns before the table's, and Postgres names a `CASE` column after the expression in
its `ELSE`. The rows query wraps each column in a `CASE` for NULL, so in Postgres
`ORDER BY "id"` sorted by the key's *text*: `-10570` before `-10580`. The end-to-end test
caught it. Aliasing every output column would only move the clash to other names, so
recon sorts the (at most threshold-sized) rows in Go, and reads checksum children by
their index.

**Bisection runs level by level.** The engine starts with the whole key range. At each
level it asks both sides, in parallel, for the children of every range that still
disagrees. A range whose rows on both sides number at most the row threshold (default
1,000) is fetched instead of split. Matching tables cost one bounds query and one
checksum query per side. Each differing row costs O(log_b n) rounds of queries.

**Row comparison is a merge join on the key.** A key on one side only is *missing left*
or *missing right*. A key on both sides whose columns differ is *changed*, listing the
columns. Tolerant floats are fetched exactly (`%.16e`, which round-trips), parsed, and
compared with `tolerance.Within`. A float that is within tolerance but in different
buckets makes its segment look different and costs a row fetch, then compares equal.
Every other column compares by its canonical text.

**The engine is reached through two small interfaces.** A `Querier` runs a query and
returns rows of strings. A `Side` is a table on an engine. The SQL implementation of
`Side` combines a `Querier`, a `Dialect` and a schema. Production uses a `database/sql`
`Querier` (with drivers, in a later change). Tests use in-memory sides for property tests
and the engines' CLIs for an end-to-end diff across engines.

## Consequences

- Each differing segment costs a query per side per level. With `b = 16`, a single
  difference in a billion rows is found in about 8 rounds.
- A range with many scattered differences fetches many small ranges, so two limits bound
  the work. `MaxRowsFetched` (default 100,000, both sides together) is reserved from
  each range's exact row counts before it is fetched; the first range that doesn't fit
  closes the budget, and from then on disagreeing ranges are recorded, not split.
  `MaxDifferences` (default 10,000) is checked between levels, so the level in progress
  finishes and the extra differences are dropped. Either way the ranges not compared are
  returned as *unresolved*, with their row counts. Every change is therefore either
  reported or inside an unresolved range, and the result says which: *equal* needs
  neither differences nor unresolved ranges. A property test checks that accounting.
- Non-integer keys are refused for now, with an error that says so.
