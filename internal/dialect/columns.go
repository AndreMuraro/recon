package dialect

import (
	"errors"
	"fmt"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
)

// ErrTableName reports a table name recon cannot look up.
var ErrTableName = errors.New("invalid table name")

// ColumnInfo is one column as information_schema describes it.
type ColumnInfo struct {
	Name     string
	DataType string // the engine's own spelling, such as "DECIMAL(18,2)" or "numeric"
	// Precision and Scale are information_schema's numeric_precision and numeric_scale;
	// HasScale is false where numeric_scale is NULL.
	Precision, Scale int32
	HasScale         bool
	Position         int // ordinal_position, from 1
}

// MaxDecimalPrecision is the largest decimal precision recon compares: the largest
// DECIMAL every dialect's canonical text can cast to.
const MaxDecimalPrecision = 38

// ColumnsSQL returns a query for the columns of table, a name in one to three parts
// (table, schema.table or catalog.schema.table). A one-part name is looked up in the
// session's current schema. Each row is: name, data type, numeric precision, numeric
// scale and ordinal position, all as text, with the empty string for NULL. Like the
// segment queries, it has no ORDER BY: sort by position.
//
// The parts are compared as written, so a quoted mixed-case table must be given in its
// stored case.
func ColumnsSQL(d Dialect, table []string) (string, error) {
	if len(table) == 0 || len(table) > 3 {
		return "", fmt.Errorf("%w: %q has %d parts; use table, schema.table or catalog.schema.table",
			ErrTableName, strings.Join(table, "."), len(table))
	}
	for _, p := range table {
		if p == "" || strings.ContainsRune(p, 0) {
			return "", fmt.Errorf("%w: empty part or NUL byte in %q", ErrTableName, strings.Join(table, "."))
		}
	}
	lit := d.StringLiteral
	where := []string{"table_name = " + lit(table[len(table)-1])}
	switch len(table) {
	case 1:
		where = append(where, "table_schema = current_schema()")
	case 2:
		where = append(where, "table_schema = "+lit(table[0]))
	case 3:
		where = append(where, "table_schema = "+lit(table[1]), "table_catalog = "+lit(table[0]))
	}
	// Snowflake and Databricks keep an information_schema per catalog (database), so a
	// table in another catalog is looked up in that catalog's.
	from := "information_schema.columns"
	if len(table) == 3 {
		from = d.QuoteIdent(table[0]) + "." + from
	}
	textType := d.types().text
	text := func(expr string) string { return "coalesce(CAST(" + expr + " AS " + textType + "), '')" }
	return fmt.Sprintf("SELECT %s, %s, %s, %s, %s FROM %s WHERE %s",
		text("column_name"), text("data_type"), text("numeric_precision"), text("numeric_scale"),
		text("ordinal_position"), from, strings.Join(where, " AND ")), nil
}

// unsupported is the error for a column type recon does not compare.
func unsupported(d Dialect, info *ColumnInfo, why string) error {
	if why != "" {
		why = ": " + why
	}
	return fmt.Errorf("%w: %s column %q has type %s%s", ErrUnsupported, d.Name(), info.Name, info.DataType, why)
}

// decimalColumn checks a decimal's precision and returns its canonical column type.
func decimalColumn(d Dialect, info *ColumnInfo) (canon.Type, int32, error) {
	switch {
	case !info.HasScale:
		return canon.TypeInvalid, 0, unsupported(d, info, "a decimal without a declared scale; compare a view that casts it")
	case info.Precision > MaxDecimalPrecision:
		return canon.TypeInvalid, 0, unsupported(d, info, fmt.Sprintf("precision above %d", MaxDecimalPrecision))
	}
	return canon.TypeDecimal, info.Scale, nil
}
