package diff

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/AndreMuraro/recon/internal/dialect"
)

// ErrResult reports a query result of the wrong shape.
var ErrResult = errors.New("unexpected query result")

// Querier runs a query and returns every row as text. The queries [SQLSide] sends return
// only text columns, so an implementation needs no type mapping; a NULL may come back as
// any string, since every column that can be NULL is wrapped in its canonical text.
type Querier interface {
	Query(ctx context.Context, sql string) ([][]string, error)
}

// SQLSide is a [Side] on a SQL engine.
type SQLSide struct {
	q   Querier
	seg *dialect.Segmenter
}

var _ Side = (*SQLSide)(nil)

// NewSQLSide returns the side that runs seg's queries through q.
func NewSQLSide(q Querier, seg *dialect.Segmenter) *SQLSide {
	return &SQLSide{q: q, seg: seg}
}

// Bounds implements [Side].
func (s *SQLSide) Bounds(ctx context.Context) (Bounds, error) {
	rows, err := s.query(ctx, s.seg.BoundsSQL(), 4)
	if err != nil {
		return Bounds{}, err
	}
	if len(rows) != 1 {
		return Bounds{}, fmt.Errorf("%w: bounds: %d rows", ErrResult, len(rows))
	}
	r := rows[0]
	var b Bounds
	if b.Rows, err = parseInt(r[0]); err != nil {
		return Bounds{}, err
	}
	if b.Keys, err = parseInt(r[1]); err != nil {
		return Bounds{}, err
	}
	if b.Rows == 0 {
		return b, nil
	}
	if b.Min, err = parseInt(r[2]); err != nil {
		return Bounds{}, err
	}
	if b.Max, err = parseInt(r[3]); err != nil {
		return Bounds{}, err
	}
	return b, nil
}

// Checksums implements [Side].
func (s *SQLSide) Checksums(ctx context.Context, r Range, splits []int64) ([]Checksum, error) {
	rows, err := s.query(ctx, s.seg.ChecksumsSQL(r.Lo, r.Hi, splits), 3)
	if err != nil {
		return nil, err
	}
	out := make([]Checksum, len(splits)+1)
	for _, row := range rows {
		child, err := parseInt(row[0])
		if err != nil {
			return nil, err
		}
		if child < 0 || child >= int64(len(out)) || out[child].Rows != 0 {
			return nil, fmt.Errorf("%w: checksums: child %d", ErrResult, child)
		}
		if out[child].Rows, err = parseInt(row[1]); err != nil {
			return nil, err
		}
		out[child].Sum = row[2]
	}
	return out, nil
}

// Rows implements [Side].
func (s *SQLSide) Rows(ctx context.Context, r Range) ([]Row, error) {
	rows, err := s.query(ctx, s.seg.RowsSQL(r.Lo, r.Hi), s.seg.Columns())
	if err != nil {
		return nil, err
	}
	out := make([]Row, len(rows))
	for i, row := range rows {
		key, err := parseInt(row[0])
		if err != nil {
			return nil, err
		}
		out[i] = Row{Key: key, Values: row}
	}
	// The query has no ORDER BY (see dialect.Segmenter).
	slices.SortFunc(out, func(a, b Row) int { return cmpInt64(a.Key, b.Key) })
	return out, nil
}

func (s *SQLSide) query(ctx context.Context, sql string, width int) ([][]string, error) {
	rows, err := s.q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if len(r) != width {
			return nil, fmt.Errorf("%w: %d columns, want %d", ErrResult, len(r), width)
		}
	}
	return rows, nil
}

func parseInt(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: integer text %q", ErrResult, s)
	}
	return n, nil
}
