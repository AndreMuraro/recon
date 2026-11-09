package diff

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/dialect"
	"github.com/AndreMuraro/recon/internal/enginetest"
)

// The end-to-end tests run the search against real engines, reached through their
// official CLIs (package enginetest). Each is skipped unless its variable names the CLI:
//
//	RECON_DUCKDB=duckdb go test ./internal/diff -run Engine
//	RECON_POSTGRES="psql postgresql://postgres@localhost/postgres" go test ./internal/diff -run Engine
//
// With both set, one table on each engine is compared with the other as well.

// load creates a table of recs on e and returns the side that reads it.
func load(t *testing.T, e *enginetest.Engine, q *enginetest.Querier, schema *canon.Encoder, recs []record) *SQLSide {
	t.Helper()
	name := e.Table(t, q, "recon_diff")
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (id BIGINT, amount DECIMAL(18, 2), score DOUBLE PRECISION, label VARCHAR, flag BOOLEAN, day DATE, updated_at TIMESTAMP);\n", name)
	for start := 0; start < len(recs); start += 500 {
		fmt.Fprintf(&b, "INSERT INTO %s VALUES\n", name)
		for i := start; i < min(start+500, len(recs)); i++ {
			if i > start {
				b.WriteString(",\n")
			}
			b.WriteString(recs[i].sqlTuple())
		}
		b.WriteString(";\n")
	}
	if err := q.Exec(t.Context(), b.String()); err != nil {
		t.Fatalf("%s: loading %s: %v", e.Name, name, err)
	}
	seg, err := dialect.NewSegmenter(e.Dialect, []string{name}, schema, dialect.Quoted(e.Dialect))
	if err != nil {
		t.Fatal(err)
	}
	return NewSQLSide(q, seg)
}

// sqlTuple writes rec as a row of literals that both engines read the same way.
func (rec *record) sqlTuple() string {
	orNull := func(null bool, lit string) string {
		if null {
			return "NULL"
		}
		return lit
	}
	score := strconv.FormatFloat(rec.score, 'g', -1, 64)
	switch {
	case math.IsNaN(rec.score):
		score = "NaN"
	case math.IsInf(rec.score, 1):
		score = "Infinity"
	case math.IsInf(rec.score, -1):
		score = "-Infinity"
	}
	return fmt.Sprintf("(%d, %s, %s, %s, %t, DATE '%s', TIMESTAMP '%s')",
		rec.id,
		orNull(rec.amountNull, "CAST('"+rec.amountText()+"' AS DECIMAL(18, 2))"),
		orNull(rec.scoreNull, "CAST('"+score+"' AS DOUBLE PRECISION)"),
		orNull(rec.labelNull, "'"+strings.ReplaceAll(rec.label, "'", "''")+"'"),
		rec.flag,
		rec.day.Format(time.DateOnly),
		rec.at.Format("2006-01-02 15:04:05.000000"))
}

// engineCase is a pair of tables and the changes between them.
type engineCase struct {
	left, right []record
	muts        []mutation
}

func newEngineCase() engineCase {
	r := testRand(2026, 11)
	left := genRecords(r, 3000, -15_000)
	right, muts := mutate(r, left, 0.01)
	return engineCase{left: left, right: right, muts: muts}
}

// engineOptions make the search go several levels deep on the test tables.
var engineOptions = Options{Branching: 4, RowThreshold: 32}

// checkAgainstModel runs the search on SQL sides and on the memory sides holding the same
// rows: it must find the injected changes, and exactly what the model finds, texts included.
func checkAgainstModel(t *testing.T, c *engineCase, left, right Side) {
	t.Helper()
	schema := testSchema(t)
	res, err := Diff(t.Context(), schema, left, right, engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	checkMutations(t, res, c.muts)
	model, err := Diff(t.Context(), schema,
		newMemorySide(t, schema, memRows(t, c.left)), newMemorySide(t, schema, memRows(t, c.right)), engineOptions)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Differences, model.Differences) {
		t.Errorf("the engines' differences differ from the model's")
		for i := range min(len(res.Differences), len(model.Differences)) {
			if !reflect.DeepEqual(res.Differences[i], model.Differences[i]) {
				t.Errorf("first at %d:\n got %+v\nwant %+v", i, res.Differences[i], model.Differences[i])
				break
			}
		}
	}
	if res.Stats.Rounds != model.Stats.Rounds || res.Stats.RowsFetched != model.Stats.RowsFetched {
		t.Errorf("stats %+v, model %+v: the engines' checksums differ from the model's", res.Stats, model.Stats)
	}
	t.Logf("%d differences, %+v", len(res.Differences), res.Stats)
}

func runEngine(t *testing.T, e *enginetest.Engine) {
	t.Helper()
	t.Parallel()
	q := e.Open(t)
	schema := testSchema(t)
	c := newEngineCase()
	left := load(t, e, q, schema, c.left)
	right := load(t, e, q, schema, c.right)

	t.Run("finds the changes", func(t *testing.T) {
		t.Parallel()
		checkAgainstModel(t, &c, left, right)
	})
	t.Run("matches the model", func(t *testing.T) {
		t.Parallel()
		// The engine's checksums are the Go reference's, so a table equals its model
		// after the first round.
		res, err := Diff(t.Context(), schema, left, newMemorySide(t, schema, memRows(t, c.left)), engineOptions)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Equal() || res.Stats.Rounds != 1 || res.Stats.RowsFetched != 0 {
			t.Errorf("table and model differ: %d differences, %+v", len(res.Differences), res.Stats)
		}
	})
}

func TestEngineDuckDB(t *testing.T)   { runEngine(t, enginetest.DuckDB) }
func TestEnginePostgres(t *testing.T) { runEngine(t, enginetest.Postgres) }

// TestEngineDuckDBToPostgres compares a table on DuckDB with one on Postgres.
func TestEngineDuckDBToPostgres(t *testing.T) {
	t.Parallel()
	duck, pg := enginetest.DuckDB.Open(t), enginetest.Postgres.Open(t)
	schema := testSchema(t)
	c := newEngineCase()
	checkAgainstModel(t, &c,
		load(t, enginetest.DuckDB, duck, schema, c.left),
		load(t, enginetest.Postgres, pg, schema, c.right))
}
