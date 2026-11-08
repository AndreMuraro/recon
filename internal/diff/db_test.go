package diff

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"slices"
	"sync"
	"testing"
)

// fakeDriver serves fixed results through database/sql, the way a real driver does:
// each query looks up its result by its text.
type fakeDriver struct {
	mu      sync.Mutex
	results map[string]fakeResult
}

type fakeResult struct {
	cols []string
	rows [][]driver.Value
	err  error // returned by the query, or by Next after the rows when rows are set
}

var fakes = &fakeDriver{results: make(map[string]fakeResult)}

func init() { sql.Register("recon-fake", fakes) }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return fakeConn{d}, nil }

type fakeConn struct{ d *fakeDriver }

func (fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not supported") }
func (fakeConn) Close() error                        { return nil }
func (fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("not supported") }

func (c fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.d.mu.Lock()
	res, ok := c.d.results[query]
	c.d.mu.Unlock()
	if !ok {
		return nil, errors.New("unknown query")
	}
	if res.err != nil && res.rows == nil {
		return nil, res.err
	}
	return &fakeRows{res: res}, nil
}

type fakeRows struct {
	res fakeResult
	i   int
}

func (r *fakeRows) Columns() []string { return r.res.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i == len(r.res.rows) {
		if r.res.err != nil {
			return r.res.err
		}
		return io.EOF
	}
	copy(dest, r.res.rows[r.i])
	r.i++
	return nil
}

func fakeQuery(t *testing.T, res fakeResult) (*DB, string) {
	t.Helper()
	query := t.Name()
	fakes.mu.Lock()
	fakes.results[query] = res
	fakes.mu.Unlock()
	db, err := sql.Open("recon-fake", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewDB(db), query
}

func TestDBReadsEveryValueAsText(t *testing.T) {
	t.Parallel()
	q, query := fakeQuery(t, fakeResult{
		cols: []string{"a", "b", "c"},
		rows: [][]driver.Value{
			{"1", []byte("x|y"), nil},
			{int64(-5), "", "3"}, // a driver may hand back an integer for a text column
		},
	})
	got, err := q.Query(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"1", "x|y", ""}, {"-5", "", "3"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDBReturnsErrors(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	tests := []struct {
		name string
		res  fakeResult
	}{
		{"query", fakeResult{err: errBoom}},
		{"iteration", fakeResult{cols: []string{"a"}, rows: [][]driver.Value{{"1"}}, err: errBoom}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			q, query := fakeQuery(t, tt.res)
			if _, err := q.Query(t.Context(), query); !errors.Is(err, errBoom) {
				t.Errorf("got %v, want %v", err, errBoom)
			}
		})
	}
}

func TestDBDrivesASearch(t *testing.T) {
	t.Parallel()
	// An empty table on both sides: one bounds query each, through database/sql.
	q, _ := fakeQuery(t, fakeResult{})
	side := fakeSide(t, nil)
	side.q = q
	fakes.mu.Lock()
	fakes.results[side.seg.BoundsSQL()] = fakeResult{
		cols: []string{"rows", "keys", "min", "max"},
		rows: [][]driver.Value{{"0", "0", nil, nil}},
	}
	fakes.mu.Unlock()
	res, err := Diff(t.Context(), fakeSchema(t), side, side, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Equal() || res.Stats.Queries != 2 {
		t.Errorf("got %+v", res)
	}
}
