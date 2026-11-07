package dialect

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
)

// engine is what the conformance test needs to know about one engine: how to declare
// columns, how to write values, and how to run a script and read the rows back.
type engine struct {
	name      string
	envVar    string // holds the CLI command; the test is skipped without it
	dialect   Dialect
	setup     string // statements run before the table is created
	tableKind string // "TABLE" or "TEMP TABLE"
	typeName  func(testColumn) string
	literal   func(testColumn, *raw) string
	// run executes script, then a query selecting selectList FROM t ORDER BY id, and
	// returns that query's rows.
	run func(ctx context.Context, cli []string, script, selectList string) ([]map[string]any, error)
}

// genericTypes declares columns with standard SQL type names; numeric names the decimal
// type and double the 8-byte float.
func genericTypes(numeric, double, text string) func(testColumn) string {
	return func(c testColumn) string {
		switch c.spec.Type {
		case canon.TypeInt:
			return "BIGINT"
		case canon.TypeDecimal:
			return fmt.Sprintf("%s(%d, %d)", numeric, c.precision, c.spec.Scale)
		case canon.TypeFloat:
			return double
		case canon.TypeBool:
			return "BOOLEAN"
		case canon.TypeDate:
			return "DATE"
		case canon.TypeTimestamp:
			if c.zoned {
				return "TIMESTAMPTZ"
			}
			return "TIMESTAMP"
		case canon.TypeString:
			return text
		case canon.TypeInvalid:
		}
		panic("unknown type")
	}
}

// standardLiteral writes values as standard SQL literals. floatText spells a float,
// including NaN and the infinities; stringLit quotes a string.
func standardLiteral(floatText func(float64) string, stringLit func(string) string) func(testColumn, *raw) string {
	return func(c testColumn, v *raw) string {
		if v.null {
			return "NULL"
		}
		switch c.spec.Type {
		case canon.TypeInt:
			return fmt.Sprintf("CAST(%d AS BIGINT)", v.i)
		case canon.TypeDecimal:
			return fmt.Sprintf("CAST('%s' AS NUMERIC(%d, %d))", v.dec, c.precision, c.spec.Scale)
		case canon.TypeFloat:
			return floatText(v.f)
		case canon.TypeBool:
			return strings.ToUpper(strconv.FormatBool(v.b))
		case canon.TypeDate:
			return "DATE '" + v.t.Format(time.DateOnly) + "'"
		case canon.TypeTimestamp:
			if c.zoned {
				return "TIMESTAMPTZ '" + v.t.In(v.zone).Format("2006-01-02 15:04:05.000000-07:00") + "'"
			}
			return "TIMESTAMP '" + v.t.Format("2006-01-02 15:04:05.000000") + "'"
		case canon.TypeString:
			return stringLit(v.s)
		case canon.TypeInvalid:
		}
		panic("unknown type")
	}
}

// floatCast writes a float as a cast from text, with the engine's spellings of NaN and
// the infinities. Every other value is the shortest text that parses back exactly.
func floatCast(typ, nan, inf string) func(float64) string {
	return func(f float64) string {
		text := floatLiteral(f)
		switch {
		case math.IsNaN(f):
			text = nan
		case math.IsInf(f, 1):
			text = inf
		case math.IsInf(f, -1):
			text = "-" + inf
		}
		return fmt.Sprintf("CAST('%s' AS %s)", text, typ)
	}
}

func standardString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

var duckdbEngine = engine{
	name:      "duckdb",
	envVar:    "RECON_DUCKDB",
	dialect:   DuckDB{},
	setup:     "SET TimeZone = 'UTC';\n",
	tableKind: "TABLE",
	typeName:  genericTypes("DECIMAL", "DOUBLE", "VARCHAR"),
	literal:   standardLiteral(floatCast("DOUBLE", "nan", "inf"), standardString),
	run: func(ctx context.Context, cli []string, script, selectList string) ([]map[string]any, error) {
		script += "SELECT " + selectList + " FROM t ORDER BY id;\n"
		out, err := commandOutput(ctx, cli, []string{"-json"}, script)
		if err != nil {
			return nil, err
		}
		// The CLI prints one JSON array per statement that returns rows.
		return lastJSONArray(out)
	},
}

// postgresString writes an escape string (E'...'), whose meaning does not depend on
// standard_conforming_strings.
func postgresString(s string) string {
	return "E'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(s) + "'"
}

var postgresEngine = engine{
	name:    "postgres",
	envVar:  "RECON_POSTGRES",
	dialect: Postgres{},
	// A fixed session, so that nothing in the server's configuration can change the result.
	setup:     "SET TimeZone = 'UTC';\nSET DateStyle = 'ISO, MDY';\nSET extra_float_digits = 1;\n",
	tableKind: "TEMP TABLE",
	typeName:  genericTypes("NUMERIC", "DOUBLE PRECISION", "TEXT"),
	literal:   standardLiteral(floatCast("DOUBLE PRECISION", "NaN", "Infinity"), postgresString),
	run: func(ctx context.Context, cli []string, script, selectList string) ([]map[string]any, error) {
		// psql has no JSON output, so the rows are aggregated into one JSON value.
		script += "SELECT json_agg(q ORDER BY q.id) FROM (SELECT " + selectList + " FROM t) q;\n"
		out, err := commandOutput(ctx, cli, []string{"--no-psqlrc", "--quiet", "--tuples-only", "--no-align",
			"--set", "ON_ERROR_STOP=1", "--file", "-"}, script)
		if err != nil {
			return nil, err
		}
		return lastJSONArray(out)
	},
}

func TestDuckDBConformance(t *testing.T) {
	t.Parallel()
	runConformance(t, duckdbEngine)
}

func TestPostgresConformance(t *testing.T) {
	t.Parallel()
	runConformance(t, postgresEngine)
}
