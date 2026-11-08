package diff

import (
	"context"
	"database/sql"
	"fmt"
)

// DB is a [Querier] over a database/sql connection pool. Every column the queries return
// is text, so values are scanned as strings and no driver type mapping is involved.
type DB struct {
	db *sql.DB
}

var _ Querier = (*DB)(nil)

// NewDB returns a Querier over db.
func NewDB(db *sql.DB) *DB { return &DB{db: db} }

// Query implements [Querier]. A NULL is returned as the empty string.
func (q *DB) Query(ctx context.Context, query string) (out [][]string, err error) {
	rows, err := q.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := rows.Close(); err == nil && cerr != nil {
			err = cerr
		}
	}()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([]sql.NullString, len(cols))
	dest := make([]any, len(cols))
	for i := range values {
		dest[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scanning row %d: %w", len(out)+1, err)
		}
		row := make([]string, len(values))
		for i, v := range values {
			row[i] = v.String
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
