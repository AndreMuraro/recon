package dialect

import (
	"fmt"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// DuckDB renders SQL for DuckDB 2.0 or later. String literals are standard SQL: a
// backslash in them is an ordinary character.
type DuckDB struct{}

var _ Dialect = DuckDB{}

// Name implements [Dialect].
func (DuckDB) Name() string { return "duckdb" }

// QuoteIdent implements [Dialect]: double quotes, with embedded quotes doubled.
func (DuckDB) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Concat implements [Dialect].
func (DuckDB) Concat(parts []string) string {
	return "(" + strings.Join(parts, " || ") + ")"
}

// Hash60 implements [Dialect]. DuckDB reads a 0x-prefixed hex string as an integer.
func (DuckDB) Hash60(text string) string {
	return fmt.Sprintf("CAST('0x' || substr(md5(%s), 1, 15) AS BIGINT)", text)
}

// SumText implements [Dialect]: HUGEINT holds 128 bits.
func (DuckDB) SumText(expr string) string {
	return fmt.Sprintf("CAST(sum(CAST(%s AS HUGEINT)) AS VARCHAR)", expr)
}

// ValueText implements [Dialect].
func (d DuckDB) ValueText(col canon.Column, expr string) (string, error) {
	var text string
	switch col.Type {
	case canon.TypeInt:
		text = fmt.Sprintf("CAST(%s AS VARCHAR)", expr)
	case canon.TypeDecimal:
		// Casting to the column's scale pads with zeros; the encoder has already refused
		// schemas whose sides would need rounding. DuckDB prints DECIMAL without an
		// exponent and without superfluous leading zeros.
		text = fmt.Sprintf("CAST(CAST(%s AS DECIMAL(38, %d)) AS VARCHAR)", expr, col.Scale)
	case canon.TypeFloat:
		text = d.floatText(col.Tolerance, fmt.Sprintf("CAST(%s AS DOUBLE)", expr))
	case canon.TypeBool:
		text = fmt.Sprintf("CASE WHEN %s THEN 't' ELSE 'f' END", expr)
	case canon.TypeDate:
		text = fmt.Sprintf("CAST(CAST(%s AS DATE) AS VARCHAR)", expr)
	case canon.TypeTimestamp:
		// epoch_us reads a TIMESTAMP as UTC and a TIMESTAMPTZ as the instant it is.
		text = fmt.Sprintf("CAST(epoch_us(%s) AS VARCHAR)", expr)
	case canon.TypeString:
		text = fmt.Sprintf(`replace(replace(%s, '\', '\\'), '|', '\|')`, expr)
	case canon.TypeInvalid:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	default:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	}
	return fmt.Sprintf(`CASE WHEN %s IS NULL THEN '\N' ELSE %s END`, expr, text), nil
}

// floatText follows tolerance.Bucket and the canonical exact float step by step. x is a
// DOUBLE expression and is repeated, so it should be a column reference or a cast of one.
func (DuckDB) floatText(tol tolerance.Tolerance, x string) string {
	special := fmt.Sprintf("WHEN isnan(%[1]s) THEN 'nan' WHEN isinf(%[1]s) AND %[1]s > 0 THEN 'inf' WHEN isinf(%[1]s) THEN '-inf'", x)
	exact := fmt.Sprintf("CASE WHEN %[1]s = 0 THEN '%[2]s' ELSE printf('%%.%[3]de', %[1]s) END",
		x, exactFloatText(0), canon.ExactFloatDigits-1)
	switch {
	case tol.IsExact():
		return fmt.Sprintf("CASE %s ELSE %s END", special, exact)
	case tol.UsesAbsBuckets():
		q := fmt.Sprintf("(%s / %s)", x, floatLiteral(tol.AbsWidth()))
		return fmt.Sprintf("CASE %s WHEN abs(%s) >= %s THEN '=' || %s ELSE '~' || CAST(CAST(floor(%s) AS BIGINT) AS VARCHAR) END",
			special, x, floatLiteral(tol.AbsLimit()), exact, q)
	default:
		index := func(abs string) string {
			return fmt.Sprintf("CAST(CAST(floor(ln(%s) / %s) AS BIGINT) AS VARCHAR)", abs, floatLiteral(tol.LogWidth()))
		}
		return fmt.Sprintf("CASE %s WHEN %s = 0 THEN '~0~' WHEN %s < 0 THEN '~-~' || %s ELSE '~' || %s END",
			special, x, x, index("-"+x), index(x))
	}
}
