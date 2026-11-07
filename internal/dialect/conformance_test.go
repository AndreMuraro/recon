package dialect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// The conformance test runs each dialect's SQL on a real engine and checks, value by
// value, that it produces exactly what the Go reference encoder produces. Engines are
// reached through their command-line clients, so the test needs no driver and no cgo:
//
//	RECON_DUCKDB=duckdb go test ./internal/dialect -run Conformance
//
// Without the variable the test is skipped. CI installs a pinned DuckDB CLI and runs it.

// testColumn is one column of the conformance table: its canonical spec, its engine type,
// and a generator of raw values.
type testColumn struct {
	spec    canon.Column
	sqlType string
	gen     func(r *rand.Rand) raw
}

// raw is a generated value before it becomes a canon.Value and a SQL literal.
type raw struct {
	null bool
	i    int64
	dec  string
	f    float64
	b    bool
	t    time.Time
	zone *time.Location // for TIMESTAMPTZ literals: the offset the value is written in
	s    string
}

const (
	absTol = 1e-6
	relTol = 1e-9
)

func conformanceColumns() []testColumn {
	return []testColumn{
		{canon.Column{Name: "i", Type: canon.TypeInt}, "BIGINT", genInt},
		{canon.Column{Name: "d", Type: canon.TypeDecimal, Scale: 4}, "DECIMAL(18, 4)", genDecimal(14, 4)},
		{canon.Column{Name: "d0", Type: canon.TypeDecimal}, "DECIMAL(38, 0)", genDecimal(30, 0)},
		{canon.Column{Name: "f", Type: canon.TypeFloat}, "DOUBLE", genFloat(absTol)},
		{canon.Column{Name: "f_abs", Type: canon.TypeFloat, Tolerance: tolerance.Tolerance{Abs: absTol}}, "DOUBLE", genFloat(absTol)},
		{canon.Column{Name: "f_rel", Type: canon.TypeFloat, Tolerance: tolerance.Tolerance{Rel: relTol}}, "DOUBLE", genFloat(absTol)},
		{canon.Column{Name: "b", Type: canon.TypeBool}, "BOOLEAN", genBool},
		{canon.Column{Name: "dt", Type: canon.TypeDate}, "DATE", genDate},
		{canon.Column{Name: "ts", Type: canon.TypeTimestamp}, "TIMESTAMP", genTimestamp(false)},
		{canon.Column{Name: "tstz", Type: canon.TypeTimestamp}, "TIMESTAMPTZ", genTimestamp(true)},
		{canon.Column{Name: "s", Type: canon.TypeString}, "VARCHAR", genString},
	}
}

func maybeNull(r *rand.Rand) bool { return r.IntN(12) == 0 }

func genInt(r *rand.Rand) raw {
	switch r.IntN(4) {
	case 0:
		return raw{null: maybeNull(r), i: r.Int64N(2000) - 1000}
	case 1:
		return raw{i: math.MaxInt64 - r.Int64N(3)}
	case 2:
		return raw{i: math.MinInt64 + r.Int64N(3)}
	default:
		return raw{null: maybeNull(r), i: int64(r.Uint64())} //nolint:gosec // wrapping is the point: full int64 range
	}
}

func genDecimal(wholeDigits, scale int) func(*rand.Rand) raw {
	return func(r *rand.Rand) raw {
		digits := func(n int) string {
			var b strings.Builder
			for range n {
				b.WriteByte(byte('0' + r.IntN(10)))
			}
			return b.String()
		}
		s := digits(1 + r.IntN(wholeDigits))
		if places := r.IntN(scale + 1); places > 0 {
			s += "." + digits(places)
		}
		if r.IntN(2) == 0 {
			s = "-" + s
		}
		return raw{null: maybeNull(r), dec: s}
	}
}

func genFloat(width float64) func(*rand.Rand) raw {
	w := width / 2
	return func(r *rand.Rand) raw {
		var f float64
		switch r.IntN(8) {
		case 0: // any float64 bit pattern, NaN and infinities included
			f = math.Float64frombits(r.Uint64())
		case 1: // exactly on an absolute bucket edge, or one step either side of it
			edge := float64(r.Int64N(1<<40)-1<<39) * w
			f = []float64{edge, math.Nextafter(edge, math.Inf(1)), math.Nextafter(edge, math.Inf(-1))}[r.IntN(3)]
		case 2: // past the absolute bucket limit, compared exactly
			f = (r.Float64() + 1) * w * tolerance.MaxIndex * float64(1+r.IntN(1000))
		case 3:
			f = []float64{0, math.Copysign(0, -1), math.NaN(), math.Inf(1), math.Inf(-1), 5e-324, math.MaxFloat64, 1e5, 0.1}[r.IntN(9)]
		case 4:
			f = float64(r.Int64N(1_000_000) - 500_000)
		default:
			f = (r.Float64()*2 - 1) * math.Pow(10, float64(r.IntN(40)-20))
		}
		return raw{null: maybeNull(r), f: f}
	}
}

func genBool(r *rand.Rand) raw { return raw{null: maybeNull(r), b: r.IntN(2) == 0} }

var (
	firstDay = time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	lastDay  = time.Date(9999, time.December, 31, 0, 0, 0, 0, time.UTC)
)

func genDate(r *rand.Rand) raw {
	switch r.IntN(6) {
	case 0:
		return raw{t: firstDay}
	case 1:
		return raw{t: lastDay}
	default:
		days := r.Int64N(int64(lastDay.Sub(firstDay)/(24*time.Hour)) + 1)
		return raw{null: maybeNull(r), t: firstDay.AddDate(0, 0, int(days))}
	}
}

func genTimestamp(withZone bool) func(*rand.Rand) raw {
	return func(r *rand.Rand) raw {
		span := lastDay.Add(24*time.Hour-time.Microsecond).UnixMicro() - firstDay.UnixMicro()
		t := time.UnixMicro(firstDay.UnixMicro() + r.Int64N(span+1)).UTC()
		if r.IntN(5) == 0 {
			t = time.UnixMicro(r.Int64N(2_000_000) - 1_000_000).UTC() // around the epoch
		}
		v := raw{null: maybeNull(r), t: t, zone: time.UTC}
		if withZone && t.Year() > 1 && t.Year() < 9999 {
			v.zone = time.FixedZone("", (r.IntN(27)-12)*3600+r.IntN(2)*1800)
		}
		return v
	}
}

func genString(r *rand.Rand) raw {
	alphabet := []string{"a", "Z", "0", " ", "|", `\`, "'", `"`, "N", "\t", "\n", "ç", "ã", "日本", "😀", " "}
	var b strings.Builder
	for range r.IntN(12) {
		b.WriteString(alphabet[r.IntN(len(alphabet))])
	}
	return raw{null: maybeNull(r), s: b.String()}
}

// value builds the canon.Value for a raw value of the column.
func (c testColumn) value(t *testing.T, v raw) canon.Value {
	t.Helper()
	if v.null {
		return canon.Null(c.spec.Type)
	}
	var (
		out canon.Value
		err error
	)
	switch c.spec.Type {
	case canon.TypeInt:
		out = canon.Int(v.i)
	case canon.TypeDecimal:
		out, err = canon.Decimal(v.dec)
	case canon.TypeFloat:
		out = canon.Float(v.f)
	case canon.TypeBool:
		out = canon.Bool(v.b)
	case canon.TypeDate:
		out, err = canon.Date(v.t)
	case canon.TypeTimestamp:
		out, err = canon.Timestamp(v.t)
	case canon.TypeString:
		out = canon.String(v.s)
	case canon.TypeInvalid:
		t.Fatalf("column %q has no type", c.spec.Name)
	}
	if err != nil {
		t.Fatalf("column %q: %v", c.spec.Name, err)
	}
	return out
}

// duckdbLiteral renders a raw value as a DuckDB literal of the column's engine type.
func (c testColumn) duckdbLiteral(v raw) string {
	if v.null {
		return "NULL"
	}
	switch c.spec.Type {
	case canon.TypeInt:
		return fmt.Sprintf("CAST(%d AS BIGINT)", v.i)
	case canon.TypeDecimal:
		return fmt.Sprintf("CAST('%s' AS %s)", v.dec, c.sqlType)
	case canon.TypeFloat:
		switch {
		case math.IsNaN(v.f):
			return "CAST('nan' AS DOUBLE)"
		case math.IsInf(v.f, 0):
			return fmt.Sprintf("CAST('%s' AS DOUBLE)", map[bool]string{true: "inf", false: "-inf"}[v.f > 0])
		}
		return fmt.Sprintf("CAST('%s' AS DOUBLE)", floatLiteral(v.f))
	case canon.TypeBool:
		return strings.ToUpper(strconv.FormatBool(v.b))
	case canon.TypeDate:
		return "DATE '" + v.t.Format(time.DateOnly) + "'"
	case canon.TypeTimestamp:
		if c.sqlType == "TIMESTAMPTZ" {
			return "TIMESTAMPTZ '" + v.t.In(v.zone).Format("2006-01-02 15:04:05.000000-07:00") + "'"
		}
		return "TIMESTAMP '" + v.t.Format("2006-01-02 15:04:05.000000") + "'"
	case canon.TypeString:
		return "'" + strings.ReplaceAll(v.s, "'", "''") + "'"
	case canon.TypeInvalid:
	}
	panic("unknown type")
}

// runDuckDB runs a script with the DuckDB CLI named by RECON_DUCKDB (a path, optionally
// followed by arguments) and returns the rows of its last query, as JSON objects.
func runDuckDB(t *testing.T, script string) []map[string]any {
	t.Helper()
	cli := strings.Fields(os.Getenv("RECON_DUCKDB"))
	if len(cli) == 0 {
		t.Skip("set RECON_DUCKDB to a DuckDB CLI to run the DuckDB conformance test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cli[0], append(cli[1:], "-json")...) //nolint:gosec // the command comes from the test's own environment
	cmd.Stdin = strings.NewReader(script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("duckdb: %v\n%s", err, stderr.String())
	}
	// The CLI prints one JSON array per statement that returns rows; keep the last.
	var rows []map[string]any
	dec := json.NewDecoder(&stdout)
	for {
		var result []map[string]any
		if err := dec.Decode(&result); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("duckdb output is not a sequence of JSON arrays: %v", err)
		}
		rows = result
	}
	return rows
}

func TestDuckDBConformance(t *testing.T) {
	t.Parallel()
	const rows = 4000
	d := DuckDB{}
	cols := conformanceColumns()
	specs := make([]canon.Column, len(cols))
	for i, c := range cols {
		specs[i] = c.spec
	}
	enc, err := canon.NewEncoder(specs)
	if err != nil {
		t.Fatal(err)
	}

	// The table and its contents.
	rng := rand.New(rand.NewPCG(2026, 11)) //nolint:gosec // deterministic test data, not security
	data := make([][]raw, rows)
	var script strings.Builder
	script.WriteString("SET TimeZone = 'UTC';\nCREATE TABLE t (id INTEGER")
	for _, c := range cols {
		fmt.Fprintf(&script, ", %s %s", d.QuoteIdent(c.spec.Name), c.sqlType)
	}
	script.WriteString(");\n")
	for id := range rows {
		data[id] = make([]raw, len(cols))
		lits := make([]string, len(cols))
		for j, c := range cols {
			data[id][j] = c.gen(rng)
			lits[j] = c.duckdbLiteral(data[id][j])
		}
		fmt.Fprintf(&script, "INSERT INTO t VALUES (%d, %s);\n", id, strings.Join(lits, ", "))
	}

	// One text expression per column, then the row's text and checksum.
	exprs := make([]string, len(cols))
	selects := []string{"id"}
	for j, c := range cols {
		exprs[j] = d.QuoteIdent(c.spec.Name)
		text, err := d.ValueText(c.spec, exprs[j])
		if err != nil {
			t.Fatal(err)
		}
		selects = append(selects, fmt.Sprintf("%s AS c%d", text, j))
	}
	rowText, err := RowText(d, enc, exprs)
	if err != nil {
		t.Fatal(err)
	}
	rowHash, err := RowHash(d, enc, exprs)
	if err != nil {
		t.Fatal(err)
	}
	selects = append(selects, rowText+" AS row_text", "CAST("+rowHash+" AS VARCHAR) AS row_hash")
	fmt.Fprintf(&script, "SELECT %s FROM t ORDER BY id;\n", strings.Join(selects, ", "))

	got := runDuckDB(t, script.String())
	if len(got) != rows {
		t.Fatalf("duckdb returned %d rows, want %d", len(got), rows)
	}

	columnEncoders := make([]*canon.Encoder, len(cols))
	for j, c := range cols {
		if columnEncoders[j], err = canon.NewEncoder([]canon.Column{c.spec}); err != nil {
			t.Fatal(err)
		}
	}
	var boundary, failures int
	for id, row := range got {
		values := make([]canon.Value, len(cols))
		rowComparable := true
		for j, c := range cols {
			values[j] = c.value(t, data[id][j])
			want, err := columnEncoders[j].AppendRow(nil, values[j:j+1])
			if err != nil {
				t.Fatal(err)
			}
			text, _ := row[fmt.Sprintf("c%d", j)].(string)
			if text == string(want) {
				continue
			}
			if onLogBucketEdge(c.spec.Tolerance, data[id][j]) {
				// ln is rounded differently across math libraries, so a value within
				// about 1e-9 of a log-bucket edge can land on either side. That costs a
				// row-level check, never a missed difference (ADR 0001).
				boundary++
				rowComparable = false
				continue
			}
			failures++
			t.Errorf("row %d, column %q (%s): duckdb %q, reference %q", id, c.spec.Name, c.duckdbLiteral(data[id][j]), text, want)
		}
		if !rowComparable {
			continue
		}
		wantText, err := enc.AppendRow(nil, values)
		if err != nil {
			t.Fatal(err)
		}
		wantHash, err := enc.Hash60(values)
		if err != nil {
			t.Fatal(err)
		}
		if row["row_text"] != string(wantText) || row["row_hash"] != strconv.FormatUint(wantHash, 10) {
			failures++
			t.Errorf("row %d: duckdb %q (hash %v), reference %q (hash %d)", id, row["row_text"], row["row_hash"], wantText, wantHash)
		}
		if failures > 20 {
			t.Fatal("too many differences; stopping")
		}
	}
	if boundary > rows/1000 {
		t.Errorf("%d values fell on a log-bucket edge; expected well under 0.1%%", boundary)
	}
	t.Logf("%d rows × %d columns matched the reference; %d values on a log-bucket edge", rows, len(cols), boundary)
}

// onLogBucketEdge reports whether v sits so close to a relative-tolerance bucket edge that
// logarithms rounded differently, by a few units in the last place, may put it on either
// side. Different math libraries round ln differently; DuckDB uses the C library's.
func onLogBucketEdge(tol tolerance.Tolerance, v raw) bool {
	if tol.IsExact() || tol.UsesAbsBuckets() || v.null || v.f == 0 || math.IsNaN(v.f) || math.IsInf(v.f, 0) {
		return false
	}
	ln := tolerance.LogAbs(v.f)
	q := ln / tol.LogWidth()
	ulp := func(x float64) float64 { return math.Nextafter(math.Abs(x), math.Inf(1)) - math.Abs(x) }
	window := 4*ulp(ln)/tol.LogWidth() + 4*ulp(q)
	return math.Abs(q-math.Round(q)) <= window
}
