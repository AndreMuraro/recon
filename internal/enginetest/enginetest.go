// Package enginetest reaches real engines through their official command-line clients,
// for tests (ADR 0003). An engine is used only when its environment variable names the
// client; otherwise the test is skipped, so `go test ./...` needs no engine:
//
//	RECON_DUCKDB=duckdb
//	RECON_POSTGRES="psql postgresql://postgres@localhost/postgres"
//
// Results are read in CSV, one client process per query.
package enginetest

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AndreMuraro/recon/internal/dialect"
)

// Querier runs SQL through an engine's client. It implements the Querier interfaces of
// packages diff and schema.
type Querier struct {
	cli []string // the client and its arguments; the script is read from stdin
	// readCLI, when set, runs queries instead of cli: DuckDB lets several processes open
	// a database file only when all of them are read-only.
	readCLI []string
}

// Exec runs a script of statements and discards their output.
func (q *Querier) Exec(ctx context.Context, script string) error {
	_, err := run(ctx, q.cli, script)
	return err
}

// Query runs one query and returns its rows as text. NULL comes back as the client
// prints it.
func (q *Querier) Query(ctx context.Context, query string) ([][]string, error) {
	cli := q.cli
	if q.readCLI != nil {
		cli = q.readCLI
	}
	out, err := run(ctx, cli, query+";\n")
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(out))
	r.FieldsPerRecord = -1
	return r.ReadAll()
}

func run(ctx context.Context, cli []string, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, cli[0], cli[1:]...) //nolint:gosec // the command comes from the test's own environment
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", filepath.Base(cli[0]), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// Engine is an engine tests can create tables in.
type Engine struct {
	Name    string
	Dialect dialect.Dialect
	envVar  string
	open    func(t testing.TB, cli []string) *Querier
	shared  bool // tables outlive the test unless dropped
}

// DuckDB runs the DuckDB CLI on a database file of the test's own.
var DuckDB = &Engine{
	Name:    "duckdb",
	Dialect: dialect.DuckDB{},
	envVar:  "RECON_DUCKDB",
	open: func(t testing.TB, cli []string) *Querier {
		t.Helper()
		db := filepath.Join(t.TempDir(), "recon.duckdb")
		args := []string{"-bail", "-csv", "-noheader"}
		return &Querier{
			cli:     append(append(slices.Clip(cli), args...), db),
			readCLI: append(append(slices.Clip(cli), args...), "-readonly", db),
		}
	},
}

// Postgres runs psql against a server shared by every test: each test's tables have
// names of their own and are dropped when it ends.
var Postgres = &Engine{
	Name:    "postgres",
	Dialect: dialect.Postgres{},
	envVar:  "RECON_POSTGRES",
	open: func(_ testing.TB, cli []string) *Querier {
		return &Querier{cli: append(slices.Clip(cli),
			"--no-psqlrc", "--quiet", "--tuples-only", "--csv",
			"--set", "ON_ERROR_STOP=1", "--file", "-")}
	},
	shared: true,
}

// Open returns a Querier for the engine, or skips the test when its variable is unset.
func (e *Engine) Open(t testing.TB) *Querier {
	t.Helper()
	cli := strings.Fields(os.Getenv(e.envVar))
	if len(cli) == 0 {
		t.Skip(e.envVar + " is not set")
	}
	return e.open(t, cli)
}

var tables atomic.Int64

// Table returns a table name that no other test, in this run or a concurrent one, uses.
// On a shared server the table is dropped when the test ends. The name is prefix plus
// digits: quote it in SQL when prefix needs quoting.
func (e *Engine) Table(t testing.TB, q *Querier, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), tables.Add(1))
	if e.shared {
		t.Cleanup(func() {
			if err := q.Exec(context.Background(), "DROP TABLE IF EXISTS "+e.Dialect.QuoteIdent(name)+";"); err != nil {
				t.Errorf("dropping %s: %v", name, err)
			}
		})
	}
	return name
}
