package dialect

import (
	"fmt"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// Postgres renders SQL for PostgreSQL 14 or later: from 14, extract(epoch ...) returns an
// exact numeric, so timestamps keep their microseconds far from 1970.
//
// Literals containing a backslash are written as escape strings (E'...'), so the SQL
// means the same whatever standard_conforming_strings is set to.
type Postgres struct{}

var _ Dialect = Postgres{}

// Name implements [Dialect].
func (Postgres) Name() string { return "postgres" }

// QuoteIdent implements [Dialect]: double quotes, with embedded quotes doubled.
func (Postgres) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (Postgres) types() sqlTypes { return sqlTypes{double: "double precision", text: "text"} }

// Concat implements [Dialect].
func (Postgres) Concat(parts []string) string {
	return "(" + strings.Join(parts, " || ") + ")"
}

// Hash60 implements [Dialect]: 15 hex digits are 60 bits, which bit(60) holds exactly.
func (Postgres) Hash60(text string) string {
	return fmt.Sprintf("CAST(CAST('x' || substr(md5(%s), 1, 15) AS bit(60)) AS bigint)", text)
}

// SumText implements [Dialect]: the sum of bigint is numeric, which does not overflow.
func (Postgres) SumText(expr string) string {
	return fmt.Sprintf("CAST(sum(%s) AS text)", expr)
}

// StringLiteral implements [Dialect]: an escape string, whose meaning doesn't depend on
// standard_conforming_strings.
func (Postgres) StringLiteral(s string) string {
	return "E'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}

// ColumnType implements [Dialect].
func (p Postgres) ColumnType(info *ColumnInfo) (canon.Type, int32, error) {
	switch info.DataType {
	case "smallint", "integer", "bigint":
		return canon.TypeInt, 0, nil
	case "numeric":
		return decimalColumn(p, info)
	case "real", "double precision":
		return canon.TypeFloat, 0, nil
	case "boolean":
		return canon.TypeBool, 0, nil
	case "date":
		return canon.TypeDate, 0, nil
	case "timestamp without time zone", "timestamp with time zone":
		return canon.TypeTimestamp, 0, nil
	case "text", "character varying", "character":
		// character(n) is compared as Postgres reads it as text: without its padding.
		return canon.TypeString, 0, nil
	}
	return canon.TypeInvalid, 0, unsupported(p, info, "")
}

// ValueText implements [Dialect].
func (p Postgres) ValueText(col canon.Column, expr string) (string, error) {
	var text string
	switch col.Type {
	case canon.TypeInt:
		text = fmt.Sprintf("CAST(%s AS text)", expr)
	case canon.TypeDecimal:
		// numeric has no negative zero and prints at its declared scale, without an
		// exponent or superfluous leading zeros.
		text = fmt.Sprintf("CAST(CAST(%s AS numeric(38, %d)) AS text)", expr, col.Scale)
	case canon.TypeFloat:
		text = p.floatText(col.Tolerance, fmt.Sprintf("CAST(%s AS double precision)", expr))
	case canon.TypeBool:
		text = fmt.Sprintf("CASE WHEN %s THEN 't' ELSE 'f' END", expr)
	case canon.TypeDate:
		// to_char, not a cast to text: the cast depends on the DateStyle setting.
		text = fmt.Sprintf("to_char(CAST(%s AS date), 'YYYY-MM-DD')", expr)
	case canon.TypeTimestamp:
		// extract reads a timestamp as UTC and a timestamptz as the instant it is.
		text = fmt.Sprintf("CAST(CAST(extract(epoch FROM %s) * 1000000 AS bigint) AS text)", expr)
	case canon.TypeString:
		text = fmt.Sprintf(`replace(replace(%s, E'\\', E'\\\\'), '|', E'\\|')`, expr)
	case canon.TypeInvalid:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	default:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	}
	return fmt.Sprintf(`CASE WHEN %s IS NULL THEN E'\\N' ELSE %s END`, expr, text), nil
}

// floatText is the canonical text of a float column; see floatText in dialect.go.
// In Postgres NaN equals NaN, so the special values can be matched with =.
func (Postgres) floatText(tol tolerance.Tolerance, x string) string {
	special := fmt.Sprintf("WHEN %[1]s = 'NaN' THEN 'nan' WHEN %[1]s = 'Infinity' THEN 'inf' WHEN %[1]s = '-Infinity' THEN '-inf'", x)
	return floatText(tol, x, special, Postgres{}.types())
}
