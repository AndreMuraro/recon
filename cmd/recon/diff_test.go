package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/connect"
	"github.com/AndreMuraro/recon/internal/enginetest"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

func TestDiffFlags(t *testing.T) {
	t.Parallel()
	base := []string{"diff", "--left", "duckdb:///l.duckdb", "--right", "postgres://h/db", "--table", "t", "--key", "id"}
	tests := []struct {
		name   string
		args   []string
		stderr string
	}{
		{"nothing", []string{"diff"}, "missing --left, --right, --key, --table"},
		{"one table", []string{"diff", "--left", "duckdb:x", "--right", "duckdb:y", "--key", "id", "--left-table", "a"}, "missing --table"},
		{"bad format", append(base, "--format", "xml"), `--format "xml"`},
		{"bad scheme", append(base[:2:2], "mysql://h/db", "--right", "duckdb:x", "--table", "t", "--key", "id"), "the scheme must be one of"},
		{"bad table", append(base, "--left-table", `"open`), "unterminated quote"},
		{"bad tolerance", append(base, "--tolerance", "x=abs:-1"), "must be finite and not negative"},
		{"limits", append(base, "--max-rows-fetched", "0"), "must be positive"},
		{"extra argument", append(base, "extra"), `unexpected argument "extra"`},
		{"no driver", base, "this build of recon doesn't include"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(t.Context(), &defaultDeps, tt.args, &stdout, &stderr); code != exitError {
				t.Errorf("exit code %d, want %d", code, exitError)
			}
			if !strings.Contains(stderr.String(), tt.stderr) {
				t.Errorf("stderr %q lacks %q", stderr.String(), tt.stderr)
			}
		})
	}
}

func TestParseTolerances(t *testing.T) {
	t.Parallel()
	got, err := parseTolerances([]string{"a=abs:0.01", "b=rel:1e-9", "c=abs:1+rel:0.001"})
	want := map[string]tolerance.Tolerance{"a": {Abs: 0.01}, "b": {Rel: 1e-9}, "c": {Abs: 1, Rel: 0.001}}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %v, %v; want %v", got, err, want)
	}
	for _, bad := range [][]string{{"a"}, {"=abs:1"}, {"a=abs:x"}, {"a=diff:1"}, {"a=abs:0"}, {"a=rel:2"}, {"a=abs:1", "a=rel:0.1"}} {
		if _, err := parseTolerances(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// engineDeps connect URLs to engines' CLIs: the URL names the engine by its scheme, and
// the rest is ignored.
func engineDeps(queriers map[string]*enginetest.Querier) *deps {
	d := defaultDeps
	d.open = func(_ context.Context, rawURL string) (*connect.Conn, error) {
		dl, err := connect.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		q, ok := queriers[rawURL]
		if !ok {
			return nil, fmt.Errorf("no test engine for %s", rawURL)
		}
		return &connect.Conn{Engine: dl.Name(), Dialect: dl, Querier: q, Close: func() error { return nil }}, nil
	}
	clock := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { clock = clock.Add(1500 * time.Millisecond); return clock }
	return &d
}

const ordersDDL = `CREATE TABLE %s (id BIGINT, amount DECIMAL(12, 2), score DOUBLE PRECISION, note VARCHAR);
INSERT INTO %[1]s VALUES %s;`

// orders returns the rows of a 500-row orders table, each passed through change first
// when it is set. A row for which change returns false is left out.
func orders(change func(i int, amount, score, note *string) bool) string {
	var rows []string
	for i := range 500 {
		amount, score, note := fmt.Sprintf("%d.%02d", i, i%100), fmt.Sprintf("%d.5", i), fmt.Sprintf("'n%d'", i)
		if change != nil && !change(i, &amount, &score, &note) {
			continue
		}
		rows = append(rows, fmt.Sprintf("(%d, %s, %s, %s)", i, amount, score, note))
	}
	return strings.Join(rows, ", ")
}

// rightChanges are the changes the right table has: a changed amount, a score within its
// tolerance, a score beyond it, and a missing row.
func rightChanges(i int, amount, score, _ *string) bool {
	switch i {
	case 10:
		*amount = "999.99"
	case 20:
		*score += "001" // 20.5001: within abs:0.01
	case 30:
		*score = "31.5"
	case 40:
		return false
	}
	return true
}

// TestEngineDiff runs recon diff end to end on DuckDB: equal tables, differing ones in
// text and JSON, a run cut short by a limit, and a schema it refuses.
func TestEngineDiff(t *testing.T) {
	t.Parallel()
	q := enginetest.DuckDB.Open(t)
	left, right := enginetest.DuckDB.Table(t, q, "left"), enginetest.DuckDB.Table(t, q, "right")
	if err := q.Exec(t.Context(), fmt.Sprintf(ordersDDL, left, orders(nil))+
		fmt.Sprintf(ordersDDL, right, orders(rightChanges))); err != nil {
		t.Fatal(err)
	}
	// Each run gets its own deps: the fake clock is not shared between parallel runs.
	d := func() *deps { return engineDeps(map[string]*enginetest.Querier{"duckdb:left": q, "duckdb:right": q}) }
	args := func(extra ...string) []string {
		base := []string{
			"diff", "--left", "duckdb:left", "--right", "duckdb:right",
			"--left-table", left, "--right-table", right, "--key", "ID", "--tolerance", "score=abs:0.01",
		}
		return append(base, extra...)
	}

	t.Run("equal", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		a := args()
		a[8] = left // compare the left table with itself
		if code := run(t.Context(), d(), a, &stdout, &stderr); code != exitOK {
			t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "status: equal · 500 left, 500 right rows") {
			t.Errorf("stdout: %s", stdout.String())
		}
	})
	t.Run("text", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), d(), args(), &stdout, &stderr); code != exitDiffers {
			t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
		}
		for _, want := range []string{
			"status: differs · 500 left, 499 right rows · 3 differences (1 only left, 0 only right, 2 changed)",
			"changed    id=10  amount: 10.10 → 999.99",
			"changed    id=30  score: 3.0500000000000000e+01 → 3.1500000000000000e+01",
			"only-left  id=40",
		} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
			}
		}
	})
	t.Run("json to a file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "report.json")
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), d(), args("--format", "json", "--output", path), &stdout, &stderr); code != exitDiffers {
			t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "status: differs") {
			t.Errorf("no summary on stdout: %q", stdout.String())
		}
		data, err := os.ReadFile(path) //nolint:gosec // the test's own temporary file
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Status string
			Left   struct{ Name string }
			Stats  struct{ Seconds float64 }
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Status != "differs" || doc.Left.Name != "duckdb:"+left || doc.Stats.Seconds != 1.5 {
			t.Errorf("report: %s", data)
		}
	})
	t.Run("incomplete", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), d(), args("--max-rows-fetched", "1"), &stdout, &stderr); code != exitIncomplete {
			t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "undecided:") {
			t.Errorf("stdout: %s", stdout.String())
		}
	})
	t.Run("schema refused", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		if code := run(t.Context(), d(), args("--exclude", "note", "--tolerance", "amount=abs:1"), &stdout, &stderr); code != exitError {
			t.Fatalf("exit code %d", code)
		}
		if !strings.Contains(stderr.String(), `tolerance for "amount": the column is not a float`) {
			t.Errorf("stderr: %s", stderr.String())
		}
	})
	t.Run("missing table", func(t *testing.T) {
		t.Parallel()
		var stdout, stderr bytes.Buffer
		a := args()
		a[8] = "no_such_table"
		if code := run(t.Context(), d(), a, &stdout, &stderr); code != exitError {
			t.Fatalf("exit code %d", code)
		}
		if !strings.Contains(stderr.String(), "right: table not found") {
			t.Errorf("stderr: %s", stderr.String())
		}
	})
}

// TestEngineDiffDuckDBToPostgres runs recon diff across two engines.
func TestEngineDiffDuckDBToPostgres(t *testing.T) {
	t.Parallel()
	duck, pg := enginetest.DuckDB.Open(t), enginetest.Postgres.Open(t)
	left, right := enginetest.DuckDB.Table(t, duck, "recon_cli"), enginetest.Postgres.Table(t, pg, "recon_cli")
	if err := duck.Exec(t.Context(), fmt.Sprintf(ordersDDL, left, orders(nil))); err != nil {
		t.Fatal(err)
	}
	if err := pg.Exec(t.Context(), fmt.Sprintf(strings.ReplaceAll(ordersDDL, "VARCHAR", "text"), right, orders(rightChanges))); err != nil {
		t.Fatal(err)
	}
	d := engineDeps(map[string]*enginetest.Querier{"duckdb:left": duck, "postgres://right": pg})
	var stdout, stderr bytes.Buffer
	args := []string{
		"diff", "--left", "duckdb:left", "--right", "postgres://right",
		"--left-table", left, "--right-table", right, "--key", "id", "--tolerance", "score=abs:0.01",
	}
	code := run(t.Context(), d, args, &stdout, &stderr)
	if code != exitDiffers {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "3 differences (1 only left, 0 only right, 2 changed)") {
		t.Errorf("stdout: %s", stdout.String())
	}
}
