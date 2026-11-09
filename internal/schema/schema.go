// Package schema reads two tables' columns from their engines and works out how to
// compare them: which columns pair up, which canonical type each pair is compared as,
// and how each side reads its columns (ADR 0006).
package schema

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/dialect"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// Errors. Match joins every problem it finds into one error, each wrapping ErrSchema.
var (
	ErrNotFound = errors.New("table not found")
	ErrSchema   = errors.New("schemas cannot be compared")
)

// Querier runs a query whose columns are all text; see diff.Querier.
type Querier interface {
	Query(ctx context.Context, sql string) ([][]string, error)
}

// Column is one column of a table.
type Column struct {
	Name     string // as the engine stores it
	DataType string // the engine's spelling of its type
	Type     canon.Type
	Scale    int32 // for TypeDecimal
	// Unsupported is why the column cannot be compared, when Type is TypeInvalid. It is an
	// error only if the column is compared: excluded columns may have any type.
	Unsupported error
}

// Table is a table's name and columns, in their declared order.
type Table struct {
	Engine  string
	Name    []string
	Columns []Column
}

// Introspect reads a table's columns through its information_schema.
func Introspect(ctx context.Context, q Querier, d dialect.Dialect, name []string) (*Table, error) {
	query, err := dialect.ColumnsSQL(d, name)
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("%s: reading the columns of %s: %w", d.Name(), strings.Join(name, "."), err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s has no table %s, or it has no columns", ErrNotFound, d.Name(), strings.Join(name, "."))
	}
	infos := make([]dialect.ColumnInfo, len(rows))
	for i, row := range rows {
		if infos[i], err = parseInfo(row); err != nil {
			return nil, fmt.Errorf("%s: columns of %s: %w", d.Name(), strings.Join(name, "."), err)
		}
	}
	slices.SortFunc(infos, func(a, b dialect.ColumnInfo) int { return cmp.Compare(a.Position, b.Position) })

	t := &Table{Engine: d.Name(), Name: slices.Clone(name), Columns: make([]Column, len(infos))}
	for i := range infos {
		info := &infos[i]
		c := Column{Name: info.Name, DataType: info.DataType}
		c.Type, c.Scale, c.Unsupported = d.ColumnType(info)
		t.Columns[i] = c
	}
	return t, nil
}

// parseInfo reads one row of dialect.ColumnsSQL.
func parseInfo(row []string) (dialect.ColumnInfo, error) {
	if len(row) != 5 {
		return dialect.ColumnInfo{}, fmt.Errorf("%d columns in a row, want 5", len(row))
	}
	info := dialect.ColumnInfo{Name: row[0], DataType: row[1]}
	n := func(s, what string) (int32, error) {
		v, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("column %q: %s %q is not an integer", row[0], what, s)
		}
		return int32(v), nil
	}
	var err error
	if row[2] != "" {
		if info.Precision, err = n(row[2], "precision"); err != nil {
			return info, err
		}
	}
	if row[3] != "" {
		if info.Scale, err = n(row[3], "scale"); err != nil {
			return info, err
		}
		info.HasScale = true
	}
	pos, err := n(row[4], "position")
	info.Position = int(pos)
	return info, err
}

// Options say how two tables are compared.
type Options struct {
	Key     string   // the key column, an integer on both sides
	Exclude []string // columns not compared, on either side
	// Tolerances apply to float columns, by name.
	Tolerances map[string]tolerance.Tolerance
}

// Plan is how two tables are compared.
type Plan struct {
	// Encoder is the compared schema: the key, then every other column the two tables
	// share, in the left table's order, each with the type both sides are compared as.
	Encoder *canon.Encoder
	// Left and Right read each column of Encoder on their side.
	Left, Right dialect.ColumnExpr
}

// Match pairs the columns of two tables. Column names are matched without regard to
// case, since engines fold unquoted names differently, and the left table's spelling is
// used in reports. Every compared column must be on both sides with compatible types:
// the same canonical type, or an integer against a decimal, compared as the decimal.
// Decimals are compared at the larger of the two scales.
//
// All problems are reported together, so that one run lists everything to fix.
func Match(left, right *Table, ld, rd dialect.Dialect, opts *Options) (*Plan, error) {
	m := matcher{opts: opts, excluded: make(map[string]bool)}
	for _, name := range opts.Exclude {
		m.excluded[fold(name)] = true
	}
	lcols, rcols := m.index("left", left), m.index("right", right)
	m.checkOptions(lcols, rcols)

	keyFold := fold(opts.Key)
	var cols []canon.Column
	lnames := make(map[string]string) // compared name → left column's name
	rnames := make(map[string]string)
	for _, lc := range left.Columns {
		f := fold(lc.Name)
		rc, ok := rcols[f]
		switch {
		case m.excluded[f]:
			continue
		case !ok:
			m.problem("column %q is only on the left (%s); exclude it to compare the rest", lc.Name, left.describe())
			continue
		}
		col, ok := m.pair(&lc, rc)
		if !ok {
			continue
		}
		lnames[col.Name], rnames[col.Name] = lc.Name, rc.Name
		if f == keyFold {
			cols = slices.Insert(cols, 0, col)
		} else {
			cols = append(cols, col)
		}
	}
	for _, rc := range right.Columns {
		if f := fold(rc.Name); !m.excluded[f] && lcols[f] == nil {
			m.problem("column %q is only on the right (%s); exclude it to compare the rest", rc.Name, right.describe())
		}
	}
	if len(m.problems) > 0 {
		return nil, errors.Join(m.problems...)
	}
	if len(cols) == 0 || fold(cols[0].Name) != keyFold {
		return nil, fmt.Errorf("%w: the key %q is excluded or missing", ErrSchema, opts.Key)
	}
	enc, err := canon.NewEncoder(cols)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSchema, err)
	}
	return &Plan{
		Encoder: enc,
		Left:    func(c canon.Column) string { return ld.QuoteIdent(lnames[c.Name]) },
		Right:   func(c canon.Column) string { return rd.QuoteIdent(rnames[c.Name]) },
	}, nil
}

type matcher struct {
	opts     *Options
	excluded map[string]bool
	problems []error
}

func (m *matcher) problem(format string, args ...any) {
	m.problems = append(m.problems, fmt.Errorf("%w: "+format, append([]any{ErrSchema}, args...)...))
}

// index maps folded names to columns, reporting names that fold together.
func (m *matcher) index(side string, t *Table) map[string]*Column {
	out := make(map[string]*Column, len(t.Columns))
	for i := range t.Columns {
		c := &t.Columns[i]
		f := fold(c.Name)
		if prev, ok := out[f]; ok && !m.excluded[f] {
			m.problem("the %s table has columns %q and %q, which differ only in case", side, prev.Name, c.Name)
		}
		out[f] = c
	}
	return out
}

// checkOptions reports options naming columns that don't exist.
func (m *matcher) checkOptions(lcols, rcols map[string]*Column) {
	exists := func(name string) bool { f := fold(name); return lcols[f] != nil || rcols[f] != nil }
	for _, name := range m.opts.Exclude {
		if !exists(name) {
			m.problem("excluded column %q is in neither table", name)
		}
	}
	if m.excluded[fold(m.opts.Key)] {
		m.problem("the key %q is excluded", m.opts.Key)
	}
	for _, name := range slices.Sorted(maps.Keys(m.opts.Tolerances)) {
		if !exists(name) {
			m.problem("tolerance for %q: no such column", name)
		}
	}
}

// pair works out how a column present on both sides is compared.
func (m *matcher) pair(lc, rc *Column) (canon.Column, bool) {
	ok := true
	for _, s := range []struct {
		side string
		c    *Column
	}{{"left", lc}, {"right", rc}} {
		if s.c.Unsupported != nil {
			m.problems = append(m.problems, fmt.Errorf("%w: on the %s, %w; exclude it to compare the rest", ErrSchema, s.side, s.c.Unsupported))
			ok = false
		}
	}
	if !ok {
		return canon.Column{}, false
	}
	col := canon.Column{Name: lc.Name, Type: lc.Type, Scale: max(lc.Scale, rc.Scale)}
	switch {
	case lc.Type == rc.Type:
	case lc.Type == canon.TypeInt && rc.Type == canon.TypeDecimal, lc.Type == canon.TypeDecimal && rc.Type == canon.TypeInt:
		col.Type = canon.TypeDecimal
	default:
		m.problem("column %q is %s on the left and %s on the right, which recon doesn't compare; compare views that cast one side",
			lc.Name, lc.DataType, rc.DataType)
		return canon.Column{}, false
	}
	if fold(lc.Name) == fold(m.opts.Key) && col.Type != canon.TypeInt {
		m.problem("the key %q must be an integer on both sides; it is %s on the left and %s on the right",
			lc.Name, lc.DataType, rc.DataType)
		return canon.Column{}, false
	}
	for name, tol := range m.opts.Tolerances {
		if fold(name) != fold(lc.Name) {
			continue
		}
		if col.Type != canon.TypeFloat {
			m.problem("tolerance for %q: the column is not a float on both sides", lc.Name)
			return canon.Column{}, false
		}
		col.Tolerance = tol
	}
	return col, true
}

func (t *Table) describe() string { return t.Engine + " " + strings.Join(t.Name, ".") }

func fold(name string) string { return strings.ToLower(name) }
