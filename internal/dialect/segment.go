package dialect

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// ErrKey reports a schema whose key cannot be used for segments.
var ErrKey = errors.New("unsupported key")

// Segmenter renders the queries that bisection runs against one table (ADR 0004): its
// bounds, the checksums of a key range's children, and the rows of a key range. Every
// query returns text only, and none has an ORDER BY: engines resolve a name in ORDER BY
// against the output columns first, and Postgres names a CASE column after the expression
// in its ELSE, so ORDER BY "id" can sort by the key's text instead of the key. Callers
// order the results themselves.
//
// The schema's first column is the key, an integer. It is part of every row's checksum,
// so that values moved between keys change the checksum too.
type Segmenter struct {
	d       Dialect
	table   string
	key     string // SQL expression of the key
	rowHash string
	compare []string // comparison text of each column, key first
}

// NewSegmenter prepares the queries for table, a name in parts (schema, table, ...), each
// quoted separately. expr reads each column of enc on this side.
func NewSegmenter(d Dialect, table []string, enc *canon.Encoder, expr ColumnExpr) (*Segmenter, error) {
	if len(table) == 0 {
		return nil, errors.New("no table name")
	}
	cols := enc.Columns()
	if cols[0].Type != canon.TypeInt {
		return nil, fmt.Errorf("%w: key column %q is %v; v0.1 compares on one integer key", ErrKey, cols[0].Name, cols[0].Type)
	}
	parts := make([]string, len(table))
	for i, p := range table {
		parts[i] = d.QuoteIdent(p)
	}
	rowHash, err := RowHash(d, enc, expr)
	if err != nil {
		return nil, err
	}
	compare := make([]string, len(cols))
	for i, col := range cols {
		// Floats are fetched exactly, so that tolerances are applied to the values
		// themselves, not to their buckets.
		col.Tolerance = tolerance.Tolerance{}
		if compare[i], err = d.ValueText(col, expr(col)); err != nil {
			return nil, fmt.Errorf("%s: column %q: %w", d.Name(), col.Name, err)
		}
	}
	return &Segmenter{
		d:       d,
		table:   strings.Join(parts, "."),
		key:     expr(cols[0]),
		rowHash: rowHash,
		compare: compare,
	}, nil
}

// BoundsSQL returns a query for one row: the row count, the number of distinct keys, and
// the smallest and largest key (NULL's canonical text when the table is empty).
func (s *Segmenter) BoundsSQL() string {
	return fmt.Sprintf("SELECT %s, %s, %s, %s FROM %s",
		s.intText("count(*)"), s.intText("count(DISTINCT "+s.key+")"),
		s.intText("min("+s.key+")"), s.intText("max("+s.key+")"), s.table)
}

// ChecksumsSQL returns a query for the children of the key range [lo, hi], split before
// each of splits (ascending, all in (lo, hi]): one row per non-empty child, in no
// particular order, with its index, its row count and the exact sum of its row checksums.
func (s *Segmenter) ChecksumsSQL(lo, hi int64, splits []int64) string {
	child := "0"
	if len(splits) > 0 {
		var b strings.Builder
		b.WriteString("CASE")
		for i, split := range splits {
			fmt.Fprintf(&b, " WHEN %s < %s THEN %d", s.key, intLiteral(split), i)
		}
		fmt.Fprintf(&b, " ELSE %d END", len(splits))
		child = b.String()
	}
	return fmt.Sprintf("SELECT %s, %s, %s FROM (SELECT %s AS child, %s AS h FROM %s WHERE %s) AS segment GROUP BY child",
		s.intText("child"), s.intText("count(*)"), s.d.SumText("h"),
		child, s.rowHash, s.table, s.inRange(lo, hi))
}

// RowsSQL returns a query for the rows of [lo, hi], in no particular order: the
// comparison text of every column, the key first.
func (s *Segmenter) RowsSQL(lo, hi int64) string {
	return fmt.Sprintf("SELECT %s FROM %s WHERE %s",
		strings.Join(s.compare, ", "), s.table, s.inRange(lo, hi))
}

// Columns returns the number of columns RowsSQL selects.
func (s *Segmenter) Columns() int { return len(s.compare) }

func (s *Segmenter) inRange(lo, hi int64) string {
	return fmt.Sprintf("%s >= %s AND %s <= %s", s.key, intLiteral(lo), s.key, intLiteral(hi))
}

func (s *Segmenter) intText(expr string) string {
	text, err := s.d.ValueText(canon.Column{Name: "n", Type: canon.TypeInt}, expr)
	if err != nil {
		panic(err) // TypeInt is supported by every dialect
	}
	return text
}

// intLiteral writes an int64 that every engine reads as a BIGINT, including the smallest,
// whose digits alone are out of range.
func intLiteral(n int64) string {
	return "CAST('" + strconv.FormatInt(n, 10) + "' AS BIGINT)"
}
