package diff

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"math/rand/v2"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// memorySide is a Side over rows held in memory, computed with the Go reference encoding.
// It is the model the SQL sides are checked against.
type memorySide struct {
	schema  *canon.Encoder   // with tolerances: the row checksums
	columns []*canon.Encoder // one per column, without tolerances: the comparison texts
	rows    []memRow         // in key order
	queries atomic.Int64
}

func newMemorySide(t *testing.T, schema *canon.Encoder, rows []memRow) *memorySide {
	t.Helper()
	s := &memorySide{schema: schema, rows: slices.Clone(rows)}
	slices.SortStableFunc(s.rows, func(a, b memRow) int { return cmpInt64(a.key, b.key) })
	for _, col := range schema.Columns() {
		col.Tolerance = tolerance.Tolerance{}
		enc, err := canon.NewEncoder([]canon.Column{col})
		if err != nil {
			t.Fatal(err)
		}
		s.columns = append(s.columns, enc)
	}
	return s
}

// memRow is one row of a memorySide: its key and its values, key first.
type memRow struct {
	key    int64
	values []canon.Value
}

func (s *memorySide) Bounds(context.Context) (Bounds, error) {
	s.queries.Add(1)
	b := Bounds{Rows: int64(len(s.rows))}
	for i, row := range s.rows {
		if i == 0 || row.key != s.rows[i-1].key {
			b.Keys++
		}
	}
	if len(s.rows) > 0 {
		b.Min, b.Max = s.rows[0].key, s.rows[len(s.rows)-1].key
	}
	return b, nil
}

func (s *memorySide) Checksums(_ context.Context, r Range, splits []int64) ([]Checksum, error) {
	s.queries.Add(1)
	sums := make([]*big.Int, len(splits)+1)
	out := make([]Checksum, len(splits)+1)
	for _, row := range s.rows {
		key := row.key
		if key < r.Lo || key > r.Hi {
			continue
		}
		child, _ := slices.BinarySearch(splits, key)
		if child < len(splits) && splits[child] == key {
			child++
		}
		h, err := s.schema.Hash60(row.values)
		if err != nil {
			return nil, err
		}
		if sums[child] == nil {
			sums[child] = new(big.Int)
		}
		sums[child].Add(sums[child], new(big.Int).SetUint64(h))
		out[child].Rows++
	}
	for i, sum := range sums {
		if sum != nil {
			out[i].Sum = sum.String()
		}
	}
	return out, nil
}

func (s *memorySide) Rows(_ context.Context, r Range) ([]Row, error) {
	s.queries.Add(1)
	var out []Row
	for _, row := range s.rows {
		key := row.key
		if key < r.Lo || key > r.Hi {
			continue
		}
		values := make([]string, len(row.values))
		for i, v := range row.values {
			text, err := s.columns[i].AppendRow(nil, []canon.Value{v})
			if err != nil {
				return nil, err
			}
			values[i] = string(text)
		}
		out = append(out, Row{Key: key, Values: values})
	}
	return out, nil
}

// testRand returns a deterministic generator, so that a failure can be replayed.
func testRand(seed1, seed2 uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed1, seed2)) //nolint:gosec // deterministic test data, not security
}

// record is one row of the test table, kept as plain Go values so that it can be written
// both as canon.Values and as SQL literals.
type record struct {
	id         int64
	cents      int64
	amountNull bool
	score      float64
	scoreNull  bool
	label      string
	labelNull  bool
	flag       bool
	day        time.Time
	at         time.Time
}

// scoreTolerance is the tolerance on the score column.
var scoreTolerance = tolerance.Tolerance{Abs: 0.01}

func testSchema(t *testing.T) *canon.Encoder {
	t.Helper()
	enc, err := canon.NewEncoder([]canon.Column{
		{Name: "id", Type: canon.TypeInt},
		{Name: "amount", Type: canon.TypeDecimal, Scale: 2},
		{Name: "score", Type: canon.TypeFloat, Tolerance: scoreTolerance},
		{Name: "label", Type: canon.TypeString},
		{Name: "flag", Type: canon.TypeBool},
		{Name: "day", Type: canon.TypeDate},
		{Name: "updated_at", Type: canon.TypeTimestamp},
	})
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

// genRecords returns n records with keys spaced by 10, so that keys can be inserted
// between them, starting from first.
func genRecords(r *rand.Rand, n int, first int64) []record {
	const labelRunes = `ab|\' é`
	out := make([]record, n)
	for i := range out {
		label := []rune(labelRunes)
		b := make([]rune, r.IntN(6))
		for j := range b {
			b[j] = label[r.IntN(len(label))]
		}
		score := (r.Float64() - 0.5) * 1e4
		switch r.IntN(200) {
		case 0:
			score = math.NaN()
		case 1:
			score = math.Inf(1 - 2*r.IntN(2))
		}
		out[i] = record{
			id:         first + int64(i)*10,
			cents:      r.Int64N(2_000_000) - 1_000_000,
			amountNull: r.IntN(20) == 0,
			score:      score,
			scoreNull:  r.IntN(20) == 0,
			label:      string(b),
			labelNull:  r.IntN(20) == 0,
			flag:       r.IntN(2) == 0,
			day:        time.Date(1990+r.IntN(60), time.Month(1+r.IntN(12)), 1+r.IntN(28), 0, 0, 0, 0, time.UTC),
			at:         time.UnixMicro(r.Int64N(4e15) - 1e15).UTC(),
		}
	}
	return out
}

func (rec *record) amountText() string {
	sign, c := "", rec.cents
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

func (rec *record) values(t *testing.T) []canon.Value {
	t.Helper()
	amount := canon.Null(canon.TypeDecimal)
	if !rec.amountNull {
		var err error
		if amount, err = canon.Decimal(rec.amountText()); err != nil {
			t.Fatal(err)
		}
	}
	score := canon.Null(canon.TypeFloat)
	if !rec.scoreNull {
		score = canon.Float(rec.score)
	}
	label := canon.Null(canon.TypeString)
	if !rec.labelNull {
		label = canon.String(rec.label)
	}
	day, err := canon.Date(rec.day)
	if err != nil {
		t.Fatal(err)
	}
	at, err := canon.Timestamp(rec.at)
	if err != nil {
		t.Fatal(err)
	}
	return []canon.Value{canon.Int(rec.id), amount, score, label, canon.Bool(rec.flag), day, at}
}

func memRows(t *testing.T, recs []record) []memRow {
	t.Helper()
	out := make([]memRow, len(recs))
	for i := range recs {
		out[i] = memRow{key: recs[i].id, values: recs[i].values(t)}
	}
	return out
}

// mutation is a change made to the right side, with the difference it must produce.
type mutation struct {
	key  int64
	kind Kind // 0 when the change is within tolerance and must not be reported
	cols []string
}

// mutate copies recs, changes about one record in frac, and returns the copy and the
// changes made.
func mutate(r *rand.Rand, recs []record, frac float64) ([]record, []mutation) {
	var (
		out  []record
		muts []mutation
	)
	for _, rec := range recs {
		if r.Float64() >= frac {
			out = append(out, rec)
			continue
		}
		switch r.IntN(7) {
		case 0:
			muts = append(muts, mutation{key: rec.id, kind: OnlyLeft})
			continue
		case 1:
			extra := rec
			extra.id += 3
			muts = append(muts, mutation{key: extra.id, kind: OnlyRight})
			out = append(out, rec, extra)
			continue
		case 2:
			rec.label += "x"
			rec.labelNull = false
			muts = append(muts, mutation{key: rec.id, kind: Changed, cols: []string{"label"}})
		case 3:
			rec.amountNull = !rec.amountNull
			muts = append(muts, mutation{key: rec.id, kind: Changed, cols: []string{"amount"}})
		case 4, 5:
			if rec.scoreNull || math.IsNaN(rec.score) || math.IsInf(rec.score, 0) {
				out = append(out, rec)
				continue
			}
			if r.IntN(2) == 0 {
				rec.score += 0.004 // within the tolerance of 0.01
				muts = append(muts, mutation{key: rec.id})
			} else {
				rec.score += 0.5
				muts = append(muts, mutation{key: rec.id, kind: Changed, cols: []string{"score"}})
			}
		case 6:
			rec.at = rec.at.Add(time.Microsecond)
			muts = append(muts, mutation{key: rec.id, kind: Changed, cols: []string{"updated_at"}})
		}
		out = append(out, rec)
	}
	return out, muts
}

// checkMutations reports every difference that the mutations do not explain, and every
// reportable mutation that is missing.
func checkMutations(t *testing.T, res *Result, muts []mutation) {
	t.Helper()
	want := make(map[int64]mutation)
	for _, m := range muts {
		if m.kind != 0 {
			want[m.key] = m
		}
	}
	for _, d := range res.Differences {
		m, ok := want[d.Key]
		if !ok {
			t.Errorf("unexpected difference at key %d: %v %v", d.Key, d.Kind, d.Columns)
			continue
		}
		delete(want, d.Key)
		if d.Kind != m.kind || !slices.Equal(d.Columns, m.cols) {
			t.Errorf("key %d: got %v %v, want %v %v", d.Key, d.Kind, d.Columns, m.kind, m.cols)
		}
	}
	for key, m := range want {
		t.Errorf("missing difference at key %d: %v %v", key, m.kind, m.cols)
	}
}
