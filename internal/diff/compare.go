package diff

import (
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/AndreMuraro/recon/internal/canon"
)

// ErrRows reports rows a side returned that break the contract of [Side.Rows].
var ErrRows = errors.New("malformed rows")

// Kind classifies a difference.
type Kind uint8

// The kinds of difference.
const (
	OnlyLeft  Kind = iota + 1 // the key is missing on the right
	OnlyRight                 // the key is missing on the left
	Changed                   // the key is on both sides with different values
)

// String implements [fmt.Stringer].
func (k Kind) String() string {
	switch k {
	case OnlyLeft:
		return "only-left"
	case OnlyRight:
		return "only-right"
	case Changed:
		return "changed"
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Difference is one key whose rows differ.
type Difference struct {
	Key  int64
	Kind Kind
	// Columns are the names of the columns that differ, for Changed.
	Columns []string
	// Left and Right are the rows' comparison texts, key first; nil on the side where
	// the key is missing.
	Left, Right []string
}

// compareRows merge-joins two key-ordered row sets of the schema cols.
func compareRows(cols []canon.Column, left, right []Row) ([]Difference, error) {
	for _, s := range []struct {
		name string
		rows []Row
	}{{"left", left}, {"right", right}} {
		for i, r := range s.rows {
			if len(r.Values) != len(cols) {
				return nil, fmt.Errorf("%w: %s row %d has %d values for %d columns", ErrRows, s.name, r.Key, len(r.Values), len(cols))
			}
			if i > 0 && s.rows[i-1].Key >= r.Key {
				if s.rows[i-1].Key == r.Key {
					return nil, fmt.Errorf("%w: the %s side has key %d twice", ErrDuplicateKeys, s.name, r.Key)
				}
				return nil, fmt.Errorf("%w: %s rows are not in key order at %d", ErrRows, s.name, r.Key)
			}
		}
	}

	var out []Difference
	i, j := 0, 0
	for i < len(left) || j < len(right) {
		switch {
		case j == len(right) || (i < len(left) && left[i].Key < right[j].Key):
			out = append(out, Difference{Key: left[i].Key, Kind: OnlyLeft, Left: left[i].Values})
			i++
		case i == len(left) || right[j].Key < left[i].Key:
			out = append(out, Difference{Key: right[j].Key, Kind: OnlyRight, Right: right[j].Values})
			j++
		default:
			l, r := left[i], right[j]
			var changed []string
			for c, col := range cols {
				eq, err := equalText(col, l.Values[c], r.Values[c])
				if err != nil {
					return nil, fmt.Errorf("key %d: column %q: %w", l.Key, col.Name, err)
				}
				if !eq {
					changed = append(changed, col.Name)
				}
			}
			if changed != nil {
				out = append(out, Difference{Key: l.Key, Kind: Changed, Columns: changed, Left: l.Values, Right: r.Values})
			}
			i++
			j++
		}
	}
	return out, nil
}

// nullText is the canonical text of NULL.
const nullText = `\N`

// equalText compares two comparison texts of col. Canonical texts are equal exactly when
// the values are, except for floats with a tolerance, which are parsed and compared with it.
func equalText(col canon.Column, a, b string) (bool, error) {
	if a == b {
		return true, nil
	}
	if col.Type != canon.TypeFloat || col.Tolerance.IsExact() || a == nullText || b == nullText {
		return false, nil
	}
	x, err := parseFloatText(a)
	if err != nil {
		return false, err
	}
	y, err := parseFloatText(b)
	if err != nil {
		return false, err
	}
	return col.Tolerance.Within(x, y), nil
}

// parseFloatText reads an exact float's canonical text.
func parseFloatText(s string) (float64, error) {
	switch s {
	case "nan":
		return math.NaN(), nil
	case "inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: float text %q", ErrRows, s)
	}
	return f, nil
}
