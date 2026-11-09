package dialect

import (
	"fmt"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// Databricks renders SQL for Databricks SQL and Spark SQL 3.4 or later (ADR 0010), with
// or without ANSI mode: no expression it writes can overflow.
//
// String literals treat the backslash as an escape character, so every literal goes
// through StringLiteral. Identifiers are case-insensitive and stored in lower case.
type Databricks struct{}

var (
	_ Dialect      = Databricks{}
	_ ColumnReader = Databricks{}
)

// Name implements [Dialect].
func (Databricks) Name() string { return "databricks" }

// QuoteIdent implements [Dialect]: backticks, with embedded backticks doubled.
func (Databricks) QuoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// StringLiteral implements [Dialect]: backslashes and quotes are escaped with a
// backslash.
func (Databricks) StringLiteral(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}

func (Databricks) types() sqlTypes { return sqlTypes{double: "DOUBLE", text: "STRING"} }

// Concat implements [Dialect].
func (Databricks) Concat(parts []string) string {
	return "concat(" + strings.Join(parts, ", ") + ")"
}

// Hash60 implements [Dialect]: conv reads 15 hex digits, 60 bits, as a decimal string.
func (Databricks) Hash60(text string) string {
	return fmt.Sprintf("CAST(conv(substr(md5(%s), 1, 15), 16, 10) AS BIGINT)", text)
}

// SumText implements [Dialect]: DECIMAL(38, 0) holds the sum of 2^66 60-bit values.
func (Databricks) SumText(expr string) string {
	return fmt.Sprintf("CAST(sum(CAST(%s AS DECIMAL(38, 0))) AS STRING)", expr)
}

// ColumnType implements [Dialect]. information_schema spells types in upper case, with
// the short integer names (INT, not INTEGER).
func (d Databricks) ColumnType(info *ColumnInfo) (canon.Type, int32, error) {
	switch strings.ToUpper(info.DataType) {
	case "TINYINT", "BYTE", "SMALLINT", "SHORT", "INT", "INTEGER", "BIGINT", "LONG":
		return canon.TypeInt, 0, nil
	case "DECIMAL":
		return decimalColumn(d, info)
	case "FLOAT", "DOUBLE":
		return canon.TypeFloat, 0, nil
	case "BOOLEAN":
		return canon.TypeBool, 0, nil
	case "DATE":
		return canon.TypeDate, 0, nil
	case "TIMESTAMP", "TIMESTAMP_NTZ":
		return canon.TypeTimestamp, 0, nil
	case "STRING", "VARCHAR", "CHAR":
		return canon.TypeString, 0, nil
	}
	return canon.TypeInvalid, 0, unsupported(d, info, "")
}

// ReadColumn implements [ColumnReader]. A TIMESTAMP_NTZ is read as the UTC instant with
// the same wall-clock time, the way every dialect reads a timestamp without a time zone.
// Casting it to TIMESTAMP would read it in the session's time zone instead.
func (Databricks) ReadColumn(dataType, ident string) string {
	if strings.EqualFold(dataType, "TIMESTAMP_NTZ") {
		return fmt.Sprintf("timestamp_micros(timestampdiff(MICROSECOND, TIMESTAMP_NTZ '1970-01-01 00:00:00', %s))", ident)
	}
	return ident
}

// ValueText implements [Dialect].
func (d Databricks) ValueText(col canon.Column, expr string) (string, error) {
	var text string
	switch col.Type {
	case canon.TypeInt:
		text = fmt.Sprintf("CAST(%s AS STRING)", expr)
	case canon.TypeDecimal:
		// Since Spark 3.0, a decimal casts to plain text at its scale, without an exponent.
		text = fmt.Sprintf("CAST(CAST(%s AS DECIMAL(38, %d)) AS STRING)", expr, col.Scale)
	case canon.TypeFloat:
		text = d.floatText(col.Tolerance, fmt.Sprintf("CAST(%s AS DOUBLE)", expr))
	case canon.TypeBool:
		text = fmt.Sprintf("CASE WHEN %s THEN 't' ELSE 'f' END", expr)
	case canon.TypeDate:
		text = fmt.Sprintf("date_format(CAST(%s AS DATE), 'yyyy-MM-dd')", expr)
	case canon.TypeTimestamp:
		// unix_micros reads a TIMESTAMP as the instant it is; see ReadColumn for
		// TIMESTAMP_NTZ.
		text = fmt.Sprintf("CAST(unix_micros(%s) AS STRING)", expr)
	case canon.TypeString:
		text = fmt.Sprintf("replace(replace(%s, %s, %s), '|', %s)",
			expr, d.StringLiteral(`\`), d.StringLiteral(`\\`), d.StringLiteral(`\|`))
	case canon.TypeInvalid:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	default:
		return "", fmt.Errorf("%w: type %v", ErrUnsupported, col.Type)
	}
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN %s ELSE %s END", expr, d.StringLiteral(`\N`), text), nil
}

// floatText is the canonical text of a float column; see floatText in dialect.go. In
// Spark NaN equals NaN, but isnan says so plainly.
func (d Databricks) floatText(tol tolerance.Tolerance, x string) string {
	special := fmt.Sprintf("WHEN isnan(%[1]s) THEN 'nan' WHEN %[1]s = CAST('Infinity' AS DOUBLE) THEN 'inf' "+
		"WHEN %[1]s = CAST('-Infinity' AS DOUBLE) THEN '-inf'", x)
	return floatText(tol, x, special, d.types())
}
