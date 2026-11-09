package dialect

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
)

// Snowflake and Databricks can't run in CI: they are services, not CLIs to install. Their
// conformance runs as a script instead, committed under testdata/conformance and kept in
// step with the code by TestConformanceScripts. The script is one SELECT that builds the
// generated rows, computes the dialect's texts, compares them with the Go reference's,
// and returns a single row with "pass" or one row per mismatch (ADR 0010).
//
// To regenerate the scripts after changing a dialect or the encoding:
//
//	go test ./internal/dialect -run ConformanceScripts -update

var update = flag.Bool("update", false, "rewrite the conformance scripts in testdata/conformance")

// scriptRows is the number of generated rows in a script: enough to cover every case the
// generators produce many times over, small enough for Snowflake's 1 MB statement limit.
const scriptRows = 400

// scriptEngine is what a script needs to know about an engine.
type scriptEngine struct {
	dialect  Dialect
	typeName func(testColumn) string
	literal  func(testColumn, *raw) string
	// convert, when set, turns the text literal holds into the column's type: VALUES then
	// holds only string literals and NULL, for engines whose VALUES takes nothing else.
	convert func(c testColumn, expr string) string
	// setup is shown as an instruction above the statement.
	setup string
}

// Snowflake's VALUES accepts only constants that it can evaluate while compiling, which
// excludes most conversion functions. Its rows are text, converted in the next step.
var snowflakeScript = scriptEngine{
	dialect: Snowflake{},
	typeName: func(c testColumn) string {
		if c.spec.Type == canon.TypeTimestamp {
			if c.zoned {
				return "TIMESTAMP_TZ"
			}
			return "TIMESTAMP_NTZ"
		}
		return genericTypes("NUMBER", "DOUBLE", "VARCHAR")(c)
	},
	literal: func(c testColumn, v *raw) string {
		if v.null {
			return "NULL"
		}
		var text string
		switch c.spec.Type {
		case canon.TypeInt:
			text = strconv.FormatInt(v.i, 10)
		case canon.TypeDecimal:
			text = v.dec
		case canon.TypeFloat:
			text = floatLiteral(v.f)
			switch {
			case math.IsNaN(v.f):
				text = "NaN"
			case math.IsInf(v.f, 1):
				text = "inf"
			case math.IsInf(v.f, -1):
				text = "-inf"
			}
		case canon.TypeBool:
			text = strings.ToUpper(strconv.FormatBool(v.b))
		case canon.TypeDate:
			text = v.t.Format(time.DateOnly)
		case canon.TypeTimestamp:
			if c.zoned {
				text = v.t.In(v.zone).Format("2006-01-02 15:04:05.000000 -07:00")
			} else {
				text = v.t.Format("2006-01-02 15:04:05.000000")
			}
		case canon.TypeString:
			text = v.s
		case canon.TypeInvalid:
			panic("unknown type")
		}
		return Snowflake{}.StringLiteral(text)
	},
	convert: func(c testColumn, expr string) string {
		switch c.spec.Type {
		case canon.TypeInt:
			return "CAST(" + expr + " AS NUMBER(38, 0))"
		case canon.TypeDecimal:
			return fmt.Sprintf("CAST(%s AS NUMBER(%d, %d))", expr, c.precision, c.spec.Scale)
		case canon.TypeFloat:
			return "CAST(" + expr + " AS DOUBLE)"
		case canon.TypeBool:
			return "CAST(" + expr + " AS BOOLEAN)"
		case canon.TypeDate:
			return "TO_DATE(" + expr + ", 'YYYY-MM-DD')"
		case canon.TypeTimestamp:
			if c.zoned {
				return "TO_TIMESTAMP_TZ(" + expr + ", 'YYYY-MM-DD HH24:MI:SS.FF6 TZH:TZM')"
			}
			return "TO_TIMESTAMP_NTZ(" + expr + ", 'YYYY-MM-DD HH24:MI:SS.FF6')"
		case canon.TypeString:
			return expr
		case canon.TypeInvalid:
		}
		panic("unknown type")
	},
	setup: "Run in any warehouse; it reads no table.",
}

var databricksScript = scriptEngine{
	dialect: Databricks{},
	typeName: func(c testColumn) string {
		if c.spec.Type == canon.TypeTimestamp {
			if c.zoned {
				return "TIMESTAMP"
			}
			return "TIMESTAMP_NTZ"
		}
		return genericTypes("DECIMAL", "DOUBLE", "STRING")(c)
	},
	literal: func(c testColumn, v *raw) string {
		switch {
		case v.null:
			return "NULL"
		case c.spec.Type == canon.TypeTimestamp && c.zoned:
			return fmt.Sprintf("CAST('%s' AS TIMESTAMP)", v.t.In(v.zone).Format("2006-01-02 15:04:05.000000-07:00"))
		case c.spec.Type == canon.TypeTimestamp:
			return fmt.Sprintf("CAST('%s' AS TIMESTAMP_NTZ)", v.t.Format("2006-01-02 15:04:05.000000"))
		case c.spec.Type == canon.TypeInt:
			// As text: the smallest BIGINT's digits alone don't parse as a BIGINT literal.
			return fmt.Sprintf("CAST('%d' AS BIGINT)", v.i)
		case c.spec.Type == canon.TypeDecimal:
			return fmt.Sprintf("CAST('%s' AS DECIMAL(%d, %d))", v.dec, c.precision, c.spec.Scale)
		}
		return standardLiteral(floatCast("DOUBLE", "NaN", "Infinity"), Databricks{}.StringLiteral)(c, v)
	},
	setup: "Run in a SQL warehouse or a notebook %sql cell, on Databricks Runtime 13.3 or later; it reads no table.",
}

// conformanceScript writes the self-checking statement for an engine.
func conformanceScript(t *testing.T, e *scriptEngine) string {
	t.Helper()
	d := e.dialect
	c := newConformanceCase(t, scriptRows)
	// In VALUES, plain literals only where the engine converts afterwards (convert);
	// otherwise typed ones, so that VALUES needs no type inference.
	plain := e.convert != nil
	idLit := func(id int) string {
		if plain {
			return strconv.Itoa(id)
		}
		return fmt.Sprintf("CAST(%d AS BIGINT)", id)
	}
	null := func(typ string) string {
		if plain {
			return "NULL"
		}
		return "CAST(NULL AS " + typ + ")"
	}
	typed := func(col testColumn, v *raw) string {
		if v.null {
			return null(e.typeName(col))
		}
		return e.literal(col, v)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `-- recon conformance check for %[1]s (ADR 0010).
-- Generated by: go test ./internal/dialect -run ConformanceScripts -update. Don't edit.
--
-- One statement: it builds %[2]d generated rows, computes recon's canonical text for every
-- value and row with the %[1]s dialect's SQL, and compares each with what recon's Go
-- reference computes. It returns one row: "pass" with the number of rows checked, or one
-- row per mismatch. %[3]s
`, d.Name(), scriptRows, e.setup)

	// The generated rows.
	names := []string{"id"}
	for _, col := range c.cols {
		names = append(names, d.QuoteIdent(col.spec.Name))
	}
	table := "t"
	if plain {
		table = "t_text"
	}
	fmt.Fprintf(&b, "WITH %s (%s) AS (\n  SELECT * FROM VALUES\n", table, strings.Join(names, ", "))
	for id := range c.data {
		lits := []string{idLit(id)}
		for j, col := range c.cols {
			lits = append(lits, typed(col, &c.data[id][j]))
		}
		sep := ","
		if id == len(c.data)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    (%s)%s\n", strings.Join(lits, ", "), sep)
	}
	b.WriteString("),\n")
	if plain {
		converted := []string{"id"}
		for _, col := range c.cols {
			name := d.QuoteIdent(col.spec.Name)
			converted = append(converted, e.convert(col, name)+" AS "+name)
		}
		fmt.Fprintf(&b, "t AS (\n  SELECT %s\n  FROM t_text\n),\n", strings.Join(converted, ",\n    "))
	}

	// The dialect's texts.
	fmt.Fprintf(&b, "got AS (\n  SELECT id,\n    %s\n  FROM t\n),\n", strings.Join(c.selectList(t, d, e.typeName), ",\n    "))

	// The reference's texts; NULL where an engine may differ (onLogBucketEdge).
	wantNames := []string{"id"}
	for j := range c.cols {
		wantNames = append(wantNames, fmt.Sprintf("c%d", j))
	}
	wantNames = append(wantNames, "row_text", "row_hash")
	text := func(s string, ok bool) string {
		if !ok {
			return null(d.types().text)
		}
		return d.StringLiteral(s)
	}
	fmt.Fprintf(&b, "want (%s) AS (\n  SELECT * FROM VALUES\n", strings.Join(wantNames, ", "))
	for id := range c.data {
		want := c.expected(t, id)
		vals := []string{idLit(id)}
		for j := range c.cols {
			vals = append(vals, text(want.texts[j], !want.edge[j]))
		}
		vals = append(vals, text(want.rowText, want.rowChecked()), text(want.hash, want.rowChecked()))
		sep := ","
		if id == len(c.data)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    (%s)%s\n", strings.Join(vals, ", "), sep)
	}
	b.WriteString("),\n")

	// Every mismatch, one row each.
	var checks []string
	for j, name := range wantNames[1:] {
		label := name
		if j < len(c.cols) {
			label = c.cols[j].spec.Name
		}
		checks = append(checks, fmt.Sprintf(
			"  SELECT g.id, %s AS col, g.%[2]s AS got, w.%[2]s AS want FROM got g JOIN want w ON g.id = w.id "+
				"WHERE w.%[2]s IS NOT NULL AND g.%[2]s IS DISTINCT FROM w.%[2]s",
			d.StringLiteral(label), name))
	}
	fmt.Fprintf(&b, "bad AS (\n%s\n)\n", strings.Join(checks, "\n  UNION ALL\n"))
	fmt.Fprintf(&b, "SELECT 'mismatch' AS result, id, col, got, want FROM bad\nUNION ALL\n"+
		"SELECT 'pass', count(*), %[1]s, CAST(NULL AS %[2]s), CAST(NULL AS %[2]s) FROM got\n"+
		"HAVING (SELECT count(*) FROM bad) = 0 AND count(*) = %[3]d\n"+
		"ORDER BY id;\n", d.StringLiteral("all columns"), d.types().text, scriptRows)
	return b.String()
}

func TestConformanceScripts(t *testing.T) {
	t.Parallel()
	for name, e := range map[string]*scriptEngine{"snowflake": &snowflakeScript, "databricks": &databricksScript} {
		path := filepath.Join("testdata", "conformance", name+".sql")
		script := conformanceScript(t, e)
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		committed, err := os.ReadFile(path) //nolint:gosec // a fixed path under testdata
		if err != nil {
			t.Fatalf("%v; run go test ./internal/dialect -run ConformanceScripts -update", err)
		}
		if string(committed) != script {
			t.Errorf("%s is out of date; run go test ./internal/dialect -run ConformanceScripts -update", path)
		}
		if len(script) > 1<<20 {
			t.Errorf("%s is %d bytes, over Snowflake's 1 MB statement limit", path, len(script))
		}
	}
}
