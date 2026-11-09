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

// StringLiteral implements [Dialect]: DuckDB string literals are standard SQL.
func (DuckDB) StringLiteral(s string) string { return quoteString(s) }

// ColumnType implements [Dialect].
func (d DuckDB) ColumnType(info *ColumnInfo) (canon.Type, int32, error) {
	switch dt := strings.ToUpper(info.DataType); {
	case dt == "TINYINT" || dt == "SMALLINT" || dt == "INTEGER" || dt == "BIGINT" ||
		dt == "UTINYINT" || dt == "USMALLINT" || dt == "UINTEGER":
		return canon.TypeInt, 0, nil
	case dt == "UBIGINT" || dt == "HUGEINT" || dt == "UHUGEINT":
		return canon.TypeInvalid, 0, unsupported(d, info, "values may not fit in 64 bits")
	case strings.HasPrefix(dt, "DECIMAL("):
		return decimalColumn(d, info)
	case dt == "FLOAT" || dt == "DOUBLE":
		return canon.TypeFloat, 0, nil
	case dt == "BOOLEAN":
		return canon.TypeBool, 0, nil
	case dt == "DATE":
		return canon.TypeDate, 0, nil
	case dt == "TIMESTAMP" || dt == "TIMESTAMP WITH TIME ZONE" || dt == "TIMESTAMP_S" || dt == "TIMESTAMP_MS":
		return canon.TypeTimestamp, 0, nil
	case dt == "TIMESTAMP_NS":
		return canon.TypeInvalid, 0, unsupported(d, info, "nanoseconds would be truncated to the canonical microseconds")
	case dt == "VARCHAR":
		return canon.TypeString, 0, nil
	}
	return canon.TypeInvalid, 0, unsupported(d, info, "")
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

// floatText is the canonical text of a float column; see floatText in dialect.go.
func (DuckDB) floatText(tol tolerance.Tolerance, x string) string {
	special := fmt.Sprintf("WHEN isnan(%[1]s) THEN 'nan' WHEN isinf(%[1]s) AND %[1]s > 0 THEN 'inf' WHEN isinf(%[1]s) THEN '-inf'", x)
	return floatText(tol, x, special, sqlTypes{double: "DOUBLE", text: "VARCHAR"})
}
