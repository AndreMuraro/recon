package schema

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/dialect"
	"github.com/AndreMuraro/recon/internal/diff"
	"github.com/AndreMuraro/recon/internal/enginetest"
)

// The engine tests run when RECON_DUCKDB and RECON_POSTGRES name the engines' CLIs; see
// package enginetest.

// TestEngineIntrospect reads the columns of a table with every supported type, and one
// that isn't, from a table whose name needs quoting in both SQL identifiers and strings.
func TestEngineIntrospect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		e    *enginetest.Engine
		ddl  string
		want map[string]string // column → canonical type, scale for decimals; "" when unsupported
	}{
		{
			e:   enginetest.DuckDB,
			ddl: `"Id" INTEGER, amount DECIMAL(10, 2), score DOUBLE, note VARCHAR, flag BOOLEAN, day DATE, ts TIMESTAMP, ts_tz TIMESTAMPTZ, u UUID`,
			want: map[string]string{
				"Id": "int", "amount": "decimal 2", "score": "float", "note": "string", "flag": "bool",
				"day": "date", "ts": "timestamp", "ts_tz": "timestamp", "u": "",
			},
		},
		{
			e:   enginetest.Postgres,
			ddl: `"Id" integer, amount numeric(12, 3), score double precision, note varchar(20), flag boolean, day date, ts timestamp, ts_tz timestamptz, u uuid`,
			want: map[string]string{
				"Id": "int", "amount": "decimal 3", "score": "float", "note": "string", "flag": "bool",
				"day": "date", "ts": "timestamp", "ts_tz": "timestamp", "u": "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.e.Name, func(t *testing.T) {
			t.Parallel()
			q := tt.e.Open(t)
			name := tt.e.Table(t, q, `quote'back\slash`)
			ident := tt.e.Dialect.QuoteIdent(name)
			if err := q.Exec(t.Context(), fmt.Sprintf("DROP TABLE IF EXISTS %[1]s; CREATE TABLE %[1]s (%s);", ident, tt.ddl)); err != nil {
				t.Fatal(err)
			}
			tbl, err := Introspect(t.Context(), q, tt.e.Dialect, []string{name})
			if err != nil {
				t.Fatal(err)
			}
			got := make(map[string]string)
			var order []string
			for _, c := range tbl.Columns {
				order = append(order, c.Name)
				switch {
				case c.Unsupported != nil:
					got[c.Name] = ""
				case c.Type == canon.TypeDecimal:
					got[c.Name] = fmt.Sprintf("decimal %d", c.Scale)
				default:
					got[c.Name] = c.Type.String()
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("got %v\nwant %v", got, tt.want)
			}
			if order[0] != "Id" || order[len(order)-1] != "u" {
				t.Errorf("columns out of declared order: %v", order)
			}
		})
	}
}

// TestEngineDuckDBToPostgres compares two tables whose schemas differ the way rewrites
// do: names in another case, an integer against a decimal, decimals at different scales,
// and a column only one side has, excluded.
func TestEngineDuckDBToPostgres(t *testing.T) {
	t.Parallel()
	duck, pg := enginetest.DuckDB.Open(t), enginetest.Postgres.Open(t)
	left := enginetest.DuckDB.Table(t, duck, "recon_schema")
	right := enginetest.Postgres.Table(t, pg, "recon_schema")

	var lrows, rrows []string
	for i := range 300 {
		note := fmt.Sprintf("'n%d'", i)
		lrows = append(lrows, fmt.Sprintf("(%d, %d.%02d, %d, %s, uuid())", i, i, i%100, i*3, note))
		amount, qty := fmt.Sprintf("%d.%02d", i, i%100), strconv.Itoa(i*3)
		switch i {
		case 41:
			amount += "1" // a third decimal place: a real difference
		case 42:
			qty = "-1"
		}
		if i == 43 {
			continue // missing on the right
		}
		rrows = append(rrows, fmt.Sprintf("(%d, %s, %s, %s)", i, amount, qty, note))
	}
	if err := duck.Exec(t.Context(), fmt.Sprintf(`CREATE TABLE %s ("ID" INTEGER, "Amount" DECIMAL(10, 2), qty BIGINT, note VARCHAR, extra UUID); INSERT INTO %[1]s VALUES %s;`,
		left, strings.Join(lrows, ", "))); err != nil {
		t.Fatal(err)
	}
	if err := pg.Exec(t.Context(), fmt.Sprintf(`CREATE TABLE %s (id bigint, amount numeric(12, 3), qty numeric(10, 0), note text); INSERT INTO %[1]s VALUES %s;`,
		right, strings.Join(rrows, ", "))); err != nil {
		t.Fatal(err)
	}

	lt, err := Introspect(t.Context(), duck, dialect.DuckDB{}, []string{left})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := Introspect(t.Context(), pg, dialect.Postgres{}, []string{right})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Match(lt, rt, dialect.DuckDB{}, dialect.Postgres{}, &Options{Key: "id", Exclude: []string{"extra"}})
	if err != nil {
		t.Fatal(err)
	}
	ls, err := dialect.NewSegmenter(dialect.DuckDB{}, []string{left}, plan.Encoder, plan.Left)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := dialect.NewSegmenter(dialect.Postgres{}, []string{right}, plan.Encoder, plan.Right)
	if err != nil {
		t.Fatal(err)
	}
	res, err := diff.Diff(t.Context(), plan.Encoder, diff.NewSQLSide(duck, ls), diff.NewSQLSide(pg, rs),
		diff.Options{Branching: 4, RowThreshold: 16})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range res.Differences {
		got = append(got, fmt.Sprintf("%d %v %v", d.Key, d.Kind, d.Columns))
	}
	want := []string{"41 changed [Amount]", "42 changed [qty]", "43 only-left []"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if d := res.Differences[0]; d.Left[1] != "41.410" || d.Right[1] != "41.411" {
		t.Errorf("amounts compared as %q and %q, want both at scale 3", d.Left[1], d.Right[1])
	}
}
