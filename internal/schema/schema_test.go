package schema

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/dialect"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

func col(name, dataType string, typ canon.Type, scale int32) Column {
	return Column{Name: name, DataType: dataType, Type: typ, Scale: scale}
}

func pgOrders() *Table {
	return &Table{Engine: "postgres", Name: []string{"public", "orders"}, Columns: []Column{
		col("amount", "numeric", canon.TypeDecimal, 2),
		col("id", "bigint", canon.TypeInt, 0),
		col("qty", "integer", canon.TypeInt, 0),
		col("score", "double precision", canon.TypeFloat, 0),
		col("note", "text", canon.TypeString, 0),
	}}
}

func duckOrders() *Table {
	return &Table{Engine: "duckdb", Name: []string{"ORDERS"}, Columns: []Column{
		col("ID", "INTEGER", canon.TypeInt, 0),
		col("AMOUNT", "DECIMAL(18,3)", canon.TypeDecimal, 3),
		col("QTY", "DECIMAL(10,0)", canon.TypeDecimal, 0),
		col("SCORE", "DOUBLE", canon.TypeFloat, 0),
		col("NOTE", "VARCHAR", canon.TypeString, 0),
	}}
}

func TestMatch(t *testing.T) {
	t.Parallel()
	tol := tolerance.Tolerance{Abs: 0.01}
	plan, err := Match(pgOrders(), duckOrders(), dialect.Postgres{}, dialect.DuckDB{},
		&Options{Key: "ID", Tolerances: map[string]tolerance.Tolerance{"Score": tol}})
	if err != nil {
		t.Fatal(err)
	}
	want := []canon.Column{
		{Name: "id", Type: canon.TypeInt},                   // the key first, in the left's spelling
		{Name: "amount", Type: canon.TypeDecimal, Scale: 3}, // the larger scale
		{Name: "qty", Type: canon.TypeDecimal},              // an integer against a decimal
		{Name: "score", Type: canon.TypeFloat, Tolerance: tol},
		{Name: "note", Type: canon.TypeString},
	}
	if got := plan.Encoder.Columns(); !slices.Equal(got, want) {
		t.Errorf("columns:\n got %+v\nwant %+v", got, want)
	}
	if got := plan.Left(want[1]); got != `"amount"` {
		t.Errorf("left reads amount as %s", got)
	}
	if got := plan.Right(want[1]); got != `"AMOUNT"` {
		t.Errorf("right reads amount as %s", got)
	}
}

func TestMatchReportsEveryProblem(t *testing.T) {
	t.Parallel()
	left, right := pgOrders(), duckOrders()
	left.Columns = append(left.Columns,
		col("only_left", "text", canon.TypeString, 0),
		Column{Name: "doc", DataType: "jsonb", Unsupported: errors.New("postgres column \"doc\" has type jsonb")})
	right.Columns[4] = col("NOTE", "DOUBLE", canon.TypeFloat, 0)
	right.Columns = append(right.Columns,
		col("ONLY_RIGHT", "VARCHAR", canon.TypeString, 0),
		col("DOC", "VARCHAR", canon.TypeString, 0))
	_, err := Match(left, right, dialect.Postgres{}, dialect.DuckDB{}, &Options{
		Key:        "id",
		Exclude:    []string{"nope"},
		Tolerances: map[string]tolerance.Tolerance{"amount": {Abs: 1}, "ghost": {Abs: 1}},
	})
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("got %v, want %v", err, ErrSchema)
	}
	for _, want := range []string{
		`"only_left" is only on the left (postgres public.orders)`,
		`"ONLY_RIGHT" is only on the right (duckdb ORDERS)`,
		`"note" is text on the left and DOUBLE on the right`,
		`type jsonb; exclude it`,
		`excluded column "nope"`,
		`tolerance for "ghost": no such column`,
		`tolerance for "amount": the column is not a float`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

func TestMatchExcludes(t *testing.T) {
	t.Parallel()
	left, right := pgOrders(), duckOrders()
	left.Columns = append(left.Columns, Column{Name: "doc", DataType: "jsonb", Unsupported: errors.New("unsupported")})
	plan, err := Match(left, right, dialect.Postgres{}, dialect.DuckDB{},
		&Options{Key: "id", Exclude: []string{"DOC", "note", "Score"}})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(plan.Encoder.Columns()); n != 3 {
		t.Errorf("%d columns compared, want 3", n)
	}
}

func TestMatchKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{"missing", Options{Key: "order_id"}, `the key "order_id" is excluded or missing`},
		{"not an integer", Options{Key: "amount"}, `the key "amount" must be an integer`},
		{"int against decimal", Options{Key: "qty"}, `the key "qty" must be an integer`},
		{"excluded", Options{Key: "id", Exclude: []string{"ID"}}, `the key "id" is excluded`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Match(pgOrders(), duckOrders(), dialect.Postgres{}, dialect.DuckDB{}, &tt.opts)
			if !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMatchRejectsNamesDifferingInCase(t *testing.T) {
	t.Parallel()
	left := pgOrders()
	left.Columns = append(left.Columns, col("Note", "text", canon.TypeString, 0))
	_, err := Match(left, duckOrders(), dialect.Postgres{}, dialect.DuckDB{}, &Options{Key: "id"})
	if err == nil || !strings.Contains(err.Error(), `"note" and "Note", which differ only in case`) {
		t.Errorf("got %v", err)
	}
}

type fakeQuerier [][]string

func (f fakeQuerier) Query(context.Context, string) ([][]string, error) { return f, nil }

func TestIntrospect(t *testing.T) {
	t.Parallel()
	q := fakeQuerier{
		{"b", "numeric", "12", "3", "2"},
		{"a", "bigint", "64", "0", "1"},
		{"c", "jsonb", "", "", "3"},
	}
	tbl, err := Introspect(t.Context(), q, dialect.Postgres{}, []string{"s", "t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Columns) != 3 || tbl.Columns[0].Name != "a" || tbl.Columns[1].Scale != 3 ||
		tbl.Columns[1].Type != canon.TypeDecimal || tbl.Columns[2].Unsupported == nil {
		t.Errorf("got %+v", tbl.Columns)
	}
	if _, err := Introspect(t.Context(), fakeQuerier{}, dialect.Postgres{}, []string{"t"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty result: got %v, want %v", err, ErrNotFound)
	}
	if _, err := Introspect(t.Context(), fakeQuerier{{"a", "bigint", "x", "0", "1"}}, dialect.Postgres{}, []string{"t"}); err == nil {
		t.Error("a malformed precision was accepted")
	}
}
