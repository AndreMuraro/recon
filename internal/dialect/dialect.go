// Package dialect renders, for each engine, the SQL that computes a row's canonical text
// and 60-bit checksum inside the warehouse. The Go reference in package canon defines what
// that text must be; each dialect is tested against it on a real engine.
package dialect

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// ErrUnsupported reports a column the dialect cannot encode.
var ErrUnsupported = errors.New("unsupported by dialect")

// Dialect is one engine's SQL for the canonical encoding.
type Dialect interface {
	// Name is the engine's name, as used in error messages and options.
	Name() string
	// QuoteIdent quotes an identifier so that it is taken literally.
	QuoteIdent(name string) string
	// ValueText returns a VARCHAR expression for the canonical text of expr, a value of
	// col's type, including NULL.
	ValueText(col canon.Column, expr string) (string, error)
	// Concat returns an expression joining text expressions, none of which is NULL.
	Concat(parts []string) string
	// Hash60 returns a BIGINT expression for the 60-bit checksum of a text expression:
	// the first 15 hex digits of its MD5, read as an integer.
	Hash60(text string) string
	// StringLiteral renders s, which may be any data without NUL bytes, as a string
	// literal that means s whatever the session's settings.
	StringLiteral(s string) string
	// ColumnType maps a column, as information_schema describes it, to its canonical
	// type and, for decimals, scale. Types recon does not compare are ErrUnsupported.
	ColumnType(info *ColumnInfo) (canon.Type, int32, error)
	// types names the engine's float and text types, for SQL built in this package.
	types() sqlTypes
	// SumText returns a text expression for the exact sum of a BIGINT expression over a
	// group, in base 10, without overflow for up to 2^66 rows of 60-bit values. An empty
	// group sums to NULL.
	SumText(expr string) string
}

// ColumnReader is implemented by dialects that read some column types through an
// expression rather than the column itself, to bring them to what ValueText expects.
type ColumnReader interface {
	// ReadColumn returns the expression that reads ident, a quoted column whose type
	// information_schema spells dataType.
	ReadColumn(dataType, ident string) string
}

// Read returns the expression for a column on d: ident itself, or what d's ColumnReader
// makes of it.
func Read(d Dialect, dataType, ident string) string {
	if r, ok := d.(ColumnReader); ok {
		return r.ReadColumn(dataType, ident)
	}
	return ident
}

// ColumnExpr returns the SQL expression that reads a column on one side: usually its
// quoted name, or a different name when the two sides name a column differently.
type ColumnExpr func(canon.Column) string

// Quoted is the ColumnExpr that reads each column by its own name.
func Quoted(d Dialect) ColumnExpr {
	return func(c canon.Column) string { return d.QuoteIdent(c.Name) }
}

// RowText returns the expression for the canonical text of a row of enc's schema.
func RowText(d Dialect, enc *canon.Encoder, expr ColumnExpr) (string, error) {
	cols := enc.Columns()
	parts := make([]string, 0, 2*len(cols)-1)
	for i, col := range cols {
		if i > 0 {
			parts = append(parts, quoteString("|"))
		}
		text, err := d.ValueText(col, expr(col))
		if err != nil {
			return "", fmt.Errorf("%s: column %q: %w", d.Name(), col.Name, err)
		}
		parts = append(parts, text)
	}
	return d.Concat(parts), nil
}

// RowHash returns the expression for the 60-bit checksum of a row; see [RowText].
func RowHash(d Dialect, enc *canon.Encoder, expr ColumnExpr) (string, error) {
	text, err := RowText(d, enc, expr)
	if err != nil {
		return "", err
	}
	return d.Hash60(text), nil
}

// quoteString renders s as a standard SQL string literal, where only the quote is
// special. Dialects whose literals treat backslashes specially must not use it for data.
func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// floatLiteral renders f so that the engine parses back exactly the same float64: the
// shortest decimal that round-trips, which every correctly rounding parser reads back.
func floatLiteral(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// floatText returns the canonical text of x, a double expression, under tol: the special
// values, then either the exact text or tolerance.Bucket's, step by step. special holds
// the engine's WHEN clauses for NaN and the infinities, which engines test differently.
func floatText(tol tolerance.Tolerance, x, special string, t sqlTypes) string {
	exact := fmt.Sprintf("CASE WHEN %s = 0 THEN '0' ELSE %s END", x, exactFloatSQL(x, t))
	index := func(expr string) string {
		return "CAST(CAST(floor(" + expr + ") AS BIGINT) AS " + t.text + ")"
	}
	switch {
	case tol.IsExact():
		return fmt.Sprintf("CASE %s ELSE %s END", special, exact)
	case tol.UsesAbsBuckets():
		return fmt.Sprintf("CASE %s WHEN abs(%s) >= %s THEN '=' || %s ELSE '~' || %s END",
			special, x, floatLiteral(tol.AbsLimit()), exact, index(fmt.Sprintf("%s / %s", x, floatLiteral(tol.AbsWidth()))))
	default:
		logIndex := func(abs string) string {
			return index(fmt.Sprintf("ln(%s) / %s", abs, floatLiteral(tol.LogWidth())))
		}
		return fmt.Sprintf("CASE %s WHEN %s = 0 THEN '~0~' WHEN %s < 0 THEN '~-~' || %s ELSE '~' || %s END",
			special, x, x, logIndex("-"+x), logIndex(x))
	}
}

// sqlTypes names the engine types the shared SQL builders cast to.
type sqlTypes struct {
	double string // a 64-bit float
	text   string // a string of any length
}

// exactFloatSQL returns a text expression for the canonical exact text of x, a nonzero,
// finite double expression (tolerance.AppendExact): "<m>p<q>", with m the significand
// and q the exponent of its lowest bit.
//
// Only ln, power, floor, comparisons and division by powers of two are used. The last are
// exact, and powers of two are exact in every engine's power function, so the result is
// exact wherever ln is accurate to well within a unit: floor(log2|x|) is estimated from
// ln and corrected by comparing |x| with the powers of two around it. Powers stay within
// [2^-1022, 2^1023], because Postgres raises an error on float underflow and overflow:
// subnormal values, whose exponent is fixed at -1074, are scaled up by 2^1000 first.
//
// x is repeated many times, so it should be a column reference or a cast of one.
func exactFloatSQL(x string, t sqlTypes) string {
	two := "CAST(2 AS " + t.double + ")"
	a := "abs(" + x + ")"
	// g estimates e = floor(log2|x|), clamped to the normal range so that 2^g and 2^(g+1)
	// are normal floats.
	g := fmt.Sprintf("least(greatest(floor(ln(%s) / ln(%s)), -1022), 1023)", a, two)
	e := fmt.Sprintf("(CASE WHEN power(%[1]s, %[2]s) > %[3]s THEN %[2]s - 1 WHEN %[2]s >= 1023 THEN %[2]s "+
		"WHEN power(%[1]s, %[2]s + 1) <= %[3]s THEN %[2]s + 1 ELSE %[2]s END)", two, g, a)
	q := fmt.Sprintf("(greatest(%s, -1022) - 52)", e)
	m := fmt.Sprintf("(CASE WHEN %[2]s < -900 THEN (%[3]s * power(%[1]s, 1000)) / power(%[1]s, %[2]s + 1000) "+
		"ELSE %[3]s / power(%[1]s, %[2]s) END)", two, q, a)
	text := func(n string) string { return "CAST(CAST(" + n + " AS BIGINT) AS " + t.text + ")" }
	return fmt.Sprintf("(CASE WHEN %s < 0 THEN '-' ELSE '' END || %s || 'p' || %s)", x, text(m), text(q))
}
