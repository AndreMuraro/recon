# ADR 0009: The exact text of a float

**Status:** accepted · **Date:** 2026-11-09 · **Supersedes:** the float row of ADR 0001's
canonical-text table

## Context

A float compared without tolerance needs a canonical text that every engine produces
byte for byte. ADR 0001 chose C's `%.16e`: 17 significant digits, correctly rounded from
the exact binary value. DuckDB (`printf`) and Postgres (`to_char` with `EEEE`) reproduce it.

Databricks can't. Its `format_string` is Java's `Formatter`, which formats a double by
padding its shortest round-trip digits instead of rounding the exact value:

| x | C `%.16e` | Java `%.16e` |
|---|---|---|
| 0.1 | `1.0000000000000001e-01` | `1.0000000000000000e-01` |
| 1/3 | `3.3333333333333331e-01` | `3.3333333333333330e-01` |
| 5e-324 | `4.9406564584124654e-324` | `4.9000000000000000e-324` |

Snowflake has no format model for a fixed number of significant digits in scientific
notation. And shortest-digit printing isn't portable either: Java before 19, which
Databricks runtimes use, doesn't always print the shortest digits.

## Decision

**The exact text of a float is its binary significand and exponent:** `<m>p<q>`, with
x = m·2^q, a minus sign for negative values, and:

- for normal floats, m the full 53-bit significand, in [2^52, 2^53), and q = e − 52, where
  2^e ≤ |x| < 2^(e+1);
- for subnormals, q = −1074 and m < 2^52;
- zero, of either sign, is `0`; NaN and the infinities are `nan`, `inf`, `-inf`.

Every float has exactly one text (fuzz-tested: the text reads back as the same float),
the golden values come from Python's `math.frexp`, and the Go reference is a few lines of
bit manipulation (`tolerance.AppendExact`).

**Engines compute it with arithmetic that is exact,** shared by every dialect
(`exactFloatSQL`):

1. e is estimated as `floor(ln|x| / ln 2)`, then corrected by comparing |x| with 2^e and
   2^(e+1). `ln` only needs to be accurate to well within one unit of log2, and the
   comparisons make the result exact.
2. q = max(e, −1022) − 52, and m = |x| / 2^q. Division by a power of two is exact, and
   powers of two are exact in every engine's `power`.
3. Powers stay within [2^−1022, 2^1023]: Postgres raises an error on float underflow and
   overflow. Subnormals are scaled up by 2^1000 before the division.

Only `ln`, `power`, `floor`, `greatest`, `least`, comparisons and casts are used, all of
which DuckDB, Postgres, Snowflake and Spark SQL have.

**People never read this text.** Reports decode it and show floats in shortest decimal
form, which also reads back as the same float.

## Consequences

- The exact float expression is long: about 4 KB, with the column read about 45 times. Engines evaluate it per
  row only for floats compared exactly, and only for rows they hash or fetch.
- Conformance covers the edges this depends on: powers of two and their neighbours
  across the whole exponent range, subnormals, the smallest normal and the largest float.
  Both DuckDB and Postgres match the reference on all of them.
- Absolute-tolerance buckets of huge values (`=` followed by the exact text) change with
  it. Nothing has been released, so no stored checksum changes meaning.
