package dialect

import (
	"fmt"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// Snowflake renders SQL for Snowflake (ADR 0010).
//
// String literals treat the backslash as an escape character, so every literal goes
// through StringLiteral. Unquoted identifiers are stored in upper case.
type Snowflake struct{}

var _ Dialect = Snowflake{}

// Name implements [Dialect].
func (Snowflake) Name() string { return "snowflake" }

// QuoteIdent implements [Dialect]: double quotes, with embedded quotes doubled. A quoted
// identifier is case-sensitive.
func (Snowflake) QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// StringLiteral implements [Dialect]: backslashes and quotes are escaped with a
// backslash.
func (Snowflake) StringLiteral(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}

func (Snowflake) types() sqlTypes { return sqlTypes{double: "DOUBLE", text: "VARCHAR"} }

// Concat implements [Dialect].
func (Snowflake) Concat(parts []string) string {
	return "(" + strings.Join(parts, " || ") + ")"
}

// Hash60 implements [Dialect]. MD5_NUMBER_UPPER64 is the first 16 hex digits of the MD5
// as an unsigned integer; dividing by 16 drops the 16th. The division is exact in
// NUMBER arithmetic, and FLOOR returns a scale-0 NUMBER.
func (Snowflake) Hash60(text string) string {
	return fmt.Sprintf("FLOOR(MD5_NUMBER_UPPER64(%s) / 16)", text)
}

// SumText implements [Dialect]: NUMBER(38, 0) holds the sum of 2^66 60-bit values.
func (Snowflake) SumText(expr string) string {
	return fmt.Sprintf("CAST(SUM(%s) AS VARCHAR)", expr)
}

// ColumnType implements [Dialect]. Snowflake's INTEGER is NUMBER(38, 0), so every
// scale-0 NUMBER is an integer: keys are usually declared that way. Values beyond 64 bits
// still compare correctly as text, though such a key would be refused when read.
func (s Snowflake) ColumnType(info *ColumnInfo) (canon.Type, int32, error) {
	switch strings.ToUpper(info.DataType) {
	case "NUMBER":
		if info.HasScale && info.Scale == 0 {
			return canon.TypeInt, 0, nil
		}
		return decimalColumn(s, info)
	case "FLOAT":
		return canon.TypeFloat, 0, nil
	case "BOOLEAN":
		return canon.TypeBool, 0, nil
	case "DATE":
		return canon.TypeDate, 0, nil
	case "TIMESTAMP_NTZ", "TIMESTAMP_LTZ", "TIMESTAMP_TZ":
		return canon.TypeTimestamp, 0, nil
	case "TEXT":
		return canon.TypeString, 0, nil
	}
	return canon.TypeInvalid, 0, unsupported(s, info, "")
}

// ValueText implements [Dialect].
func (s Snowflake) ValueText(col canon.Column, expr string) (string, error) {
	var text string
	switch col.Type {
	case canon.TypeInt:
		text = fmt.Sprintf("CAST(%s AS VARCHAR)", expr)
	case canon.TypeDecimal:
		// NUMBER prints at its scale, without an exponent or superfluous zeros.
		text = fmt.Sprintf("CAST(CAST(%s AS NUMBER(38, %d)) AS VARCHAR)", expr, col.Scale)
	case canon.TypeFloat:
		text = s.floatText(col.Tolerance, fmt.Sprintf("CAST(%s AS DOUBLE)", expr))
	case canon.TypeBool:
		text = fmt.Sprintf("CASE WHEN %s THEN 't' ELSE 'f' END", expr)
	case canon.TypeDate:
		text = fmt.Sprintf("TO_CHAR(CAST(%s AS DATE), 'YYYY-MM-DD')", expr)
	case canon.TypeTimestamp:
		text = s.timestampText(expr)
	case canon.TypeString:
		text = fmt.Sprintf("replace(replace(%s, %s, %s), '|', %s)",
			expr, s.StringLiteral(`\`), s.StringLiteral(`\\`), s.StringLiteral(`\|`))
	case canon.TypeInvalid:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	default:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	}
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN %s ELSE %s END", expr, s.StringLiteral(`\N`), text), nil
}

// timestampText is microseconds since the epoch, read from the exact nanoseconds:
// EPOCH_NANOSECOND reads a TIMESTAMP_NTZ as UTC and the other kinds as the instant they
// are. Snowflake timestamps can hold nanoseconds, which the canonical form can't; a value
// with any is written as "ns" and its nanoseconds, which no microsecond value's text can
// equal. So a difference below a microsecond is reported, not truncated away.
func (Snowflake) timestampText(expr string) string {
	ns := fmt.Sprintf("DATE_PART(EPOCH_NANOSECOND, %s)", expr)
	return fmt.Sprintf("CASE WHEN MOD(%[1]s, 1000) = 0 THEN CAST(FLOOR(%[1]s / 1000) AS VARCHAR) ELSE 'ns' || CAST(%[1]s AS VARCHAR) END", ns)
}

// floatText is the canonical text of a float column; see floatText in dialect.go. In
// Snowflake NaN equals NaN, so the special values can be matched with =.
func (s Snowflake) floatText(tol tolerance.Tolerance, x string) string {
	special := fmt.Sprintf("WHEN %[1]s = CAST('NaN' AS DOUBLE) THEN 'nan' WHEN %[1]s = CAST('inf' AS DOUBLE) THEN 'inf' "+
		"WHEN %[1]s = CAST('-inf' AS DOUBLE) THEN '-inf'", x)
	return floatText(tol, x, special, s.types())
}
