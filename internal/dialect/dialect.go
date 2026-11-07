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
}

// RowText returns the expression for the canonical text of a row. exprs holds one SQL
// expression per column of enc, in the same order.
func RowText(d Dialect, enc *canon.Encoder, exprs []string) (string, error) {
	cols := enc.Columns()
	if len(exprs) != len(cols) {
		return "", fmt.Errorf("%d expressions for %d columns", len(exprs), len(cols))
	}
	parts := make([]string, 0, 2*len(cols)-1)
	for i, col := range cols {
		if i > 0 {
			parts = append(parts, quoteString("|"))
		}
		text, err := d.ValueText(col, exprs[i])
		if err != nil {
			return "", fmt.Errorf("%s: column %q: %w", d.Name(), col.Name, err)
		}
		parts = append(parts, text)
	}
	return d.Concat(parts), nil
}

// RowHash returns the expression for the 60-bit checksum of a row; see [RowText].
func RowHash(d Dialect, enc *canon.Encoder, exprs []string) (string, error) {
	text, err := RowText(d, enc, exprs)
	if err != nil {
		return "", err
	}
	return d.Hash60(text), nil
}

// quoteString renders s as a standard SQL string literal. It is only ever given constants
// from this package, never data.
func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// floatLiteral renders f so that the engine parses back exactly the same float64: the
// shortest decimal that round-trips, which every correctly rounding parser reads back.
func floatLiteral(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// exactFloatText formats f the way the canonical encoding does for exact floats: C's
// %.16e. Used for the constant zero.
func exactFloatText(f float64) string {
	return strconv.FormatFloat(f, 'e', canon.ExactFloatDigits-1, 64)
}
