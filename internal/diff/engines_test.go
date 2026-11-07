package diff

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/dialect"
)

// The end-to-end tests run the search against real engines, reached through their
// official CLIs like the conformance tests in package dialect (ADR 0003). Each is skipped
// unless its variable names the CLI:
//
//	RECON_DUCKDB=duckdb go test ./internal/diff -run Engine
//	RECON_POSTGRES="psql postgresql://postgres@localhost/postgres" go test ./internal/diff -run Engine
//
// With both set, one table on each engine is compared with the other as well.

// cliQuerier is a Querier over an engine's CLI in CSV mode, one process per query.
type cliQuerier struct {
	cli []string // the command and its arguments; the script is read from stdin
	// readCLI, when set, runs queries instead of cli: DuckDB lets several processes open
	// a database file only when all of them are read-only.
	readCLI []string
}

func (q cliQuerier) run(ctx context.Context, script string) ([]byte, error) {
	return runCLI(ctx, q.cli, script)
}

func runCLI(ctx context.Context, cli []string, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli[0], cli[1:]...) //nolint:gosec // the command comes from the test's own environment
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w\n%s", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

func (q cliQuerier) Query(ctx context.Context, sql string) ([][]string, error) {
	cli := q.cli
	if q.readCLI != nil {
		cli = q.readCLI
	}
	out, err := runCLI(ctx, cli, sql+";\n")
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(out))
	r.FieldsPerRecord = -1
	return r.ReadAll()
}

// testEngine is an engine to load test tables into.
type testEngine struct {
	name    string
	dialect dialect.Dialect
	// open returns the querier for a fresh database, or skips the test.
	open func(t *testing.T) cliQuerier
	// table returns a name for a new table, dropped when the test ends.
	table func(t *testing.T, q cliQuerier) string
}

var duckdbTestEngine = testEngine{
	name:    "duckdb",
	dialect: dialect.DuckDB{},
	open: func(t *testing.T) cliQuerier {
		t.Helper()
		cli := strings.Fields(os.Getenv("RECON_DUCKDB"))
		if len(cli) == 0 {
			t.Skip("RECON_DUCKDB is not set")
		}
		db := filepath.Join(t.TempDir(), "recon.duckdb")
		args := []string{"-bail", "-csv", "-noheader"}
		return cliQuerier{
			cli:     append(append(slices.Clip(cli), args...), db),
			readCLI: append(append(slices.Clip(cli), args...), "-readonly", db),
		}
	},
	table: func(_ *testing.T, _ cliQuerier) string { return tableName("t") },
}

var postgresTestEngine = testEngine{
	name:    "postgres",
	dialect: dialect.Postgres{},
	open: func(t *testing.T) cliQuerier {
		t.Helper()
		cli := strings.Fields(os.Getenv("RECON_POSTGRES"))
		if len(cli) == 0 {
			t.Skip("RECON_POSTGRES is not set")
		}
		return cliQuerier{cli: append(cli,
			"--no-psqlrc", "--quiet", "--tuples-only", "--csv",
			"--set", "ON_ERROR_STOP=1", "--file", "-")}
	},
	// The server is shared by every test: each table has its own name and is dropped.
	table: func(t *testing.T, q cliQuerier) string {
		t.Helper()
		name := tableName("recon_diff_")
		t.Cleanup(func() {
			if _, err := q.run(context.Background(), "DROP TABLE IF EXISTS "+name+";"); err != nil {
				t.Errorf("dropping %s: %v", name, err)
			}
		})
		return name
	},
}

var tables atomic.Int64

// tableName returns a name no other table of this or a concurrent test run uses.
func tableName(prefix string) string {
	return fmt.Sprintf("%s%d_%d", prefix, os.Getpid(), tables.Add(1))
}

// load creates a table of recs and returns the side that reads it.
func (e *testEngine) load(t *testing.T, q cliQuerier, schema *canon.Encoder, recs []record) *SQLSide {
	t.Helper()
	name := e.table(t, q)
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
	if _, err := q.run(t.Context(), b.String()); err != nil {
		t.Fatalf("%s: loading %s: %v", e.name, name, err)
	}
	seg, err := dialect.NewSegmenter(e.dialect, []string{name}, schema, dialect.Quoted(e.dialect))
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

func runEngine(t *testing.T, e *testEngine) {
	t.Helper()
	t.Parallel()
	q := e.open(t)
	schema := testSchema(t)
	c := newEngineCase()
	left := e.load(t, q, schema, c.left)
	right := e.load(t, q, schema, c.right)

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

func TestEngineDuckDB(t *testing.T)   { runEngine(t, &duckdbTestEngine) }
func TestEnginePostgres(t *testing.T) { runEngine(t, &postgresTestEngine) }

// TestEngineDuckDBToPostgres compares a table on DuckDB with one on Postgres.
func TestEngineDuckDBToPostgres(t *testing.T) {
	t.Parallel()
	duck, pg := duckdbTestEngine.open(t), postgresTestEngine.open(t)
	schema := testSchema(t)
	c := newEngineCase()
	checkAgainstModel(t, &c,
		duckdbTestEngine.load(t, duck, schema, c.left),
		postgresTestEngine.load(t, pg, schema, c.right))
}
