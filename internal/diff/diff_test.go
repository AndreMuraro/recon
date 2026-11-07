package diff

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
)

func TestSplit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    Range
		n    int
		want []int64
	}{
		{"single key", Range{5, 5}, 16, nil},
		{"fewer keys than children", Range{0, 2}, 16, []int64{1, 2}},
		{"even", Range{0, 99}, 4, []int64{25, 50, 75}},
		{"uneven", Range{0, 9}, 4, []int64{2, 5, 7}},
		{"negative", Range{-10, -1}, 2, []int64{-5}},
		{"full range", Range{math.MinInt64, math.MaxInt64}, 2, []int64{0}},
		{"full range in four", Range{math.MinInt64, math.MaxInt64}, 4, []int64{math.MinInt64 / 2, 0, math.MaxInt64/2 + 1}},
		{"top", Range{math.MaxInt64 - 1, math.MaxInt64}, 16, []int64{math.MaxInt64}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.r.split(tt.n); !slices.Equal(got, tt.want) {
				t.Errorf("%+v.split(%d) = %v, want %v", tt.r, tt.n, got, tt.want)
			}
		})
	}
}

// FuzzSplit checks that the children of any range partition it, in order, without empty
// children, and are as many as asked for when the range has enough keys.
func FuzzSplit(f *testing.F) {
	f.Add(int64(0), int64(99), uint8(16))
	f.Add(int64(math.MinInt64), int64(math.MaxInt64), uint8(3))
	f.Add(int64(-7), int64(7), uint8(255))
	f.Fuzz(func(t *testing.T, lo, hi int64, n uint8) {
		if lo > hi {
			lo, hi = hi, lo
		}
		r := Range{lo, hi}
		splits := r.split(int(n))
		size := float64(hi) - float64(lo) + 1
		wantChildren := min(float64(n), size)
		if n < 2 {
			wantChildren = 1
		}
		if float64(len(splits)+1) != wantChildren {
			t.Fatalf("%+v.split(%d): %d children, want %v", r, n, len(splits)+1, wantChildren)
		}
		next := lo
		for i := range len(splits) + 1 {
			c := r.child(splits, i)
			if c.Lo != next || c.Hi < c.Lo {
				t.Fatalf("%+v.split(%d): child %d is %+v after %d", r, n, i, c, next)
			}
			if c.Hi == hi {
				if i != len(splits) {
					t.Fatalf("%+v.split(%d): child %d reaches the end early", r, n, i)
				}
				return
			}
			next = c.Hi + 1
		}
		t.Fatalf("%+v.split(%d): children end before %d", r, n, hi)
	})
}

func TestCompareRows(t *testing.T) {
	t.Parallel()
	cols := testSchema(t).Columns()
	row := func(key int64, score string) Row {
		return Row{Key: key, Values: []string{strconv.FormatInt(key, 10), "1.00", score, "a", "t", "2024-01-02", "0"}}
	}
	left := []Row{row(1, "1.0000000000000000e+00"), row(2, "nan"), row(3, `\N`), row(5, "inf"), row(6, "2.0000000000000000e+00")}
	right := []Row{row(1, "1.0050000000000000e+00"), row(2, "nan"), row(3, "0.0000000000000000e+00"), row(4, "nan"), row(5, "-inf"), row(6, "3.0000000000000000e+00")}
	got, err := compareRows(cols, left, right)
	if err != nil {
		t.Fatal(err)
	}
	type summary struct {
		key  int64
		kind Kind
		cols []string
	}
	want := []summary{
		{3, Changed, []string{"score"}}, // NULL never equals a value
		{4, OnlyRight, nil},
		{5, Changed, []string{"score"}},
		{6, Changed, []string{"score"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d differences, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if g := got[i]; g.Key != w.key || g.Kind != w.kind || !slices.Equal(g.Columns, w.cols) {
			t.Errorf("difference %d = %d %v %v, want %+v", i, g.Key, g.Kind, g.Columns, w)
		}
	}
}

func TestCompareRowsRejectsMalformedRows(t *testing.T) {
	t.Parallel()
	cols := testSchema(t).Columns()
	ok := func(key int64) Row { return Row{Key: key, Values: make([]string, len(cols))} }
	tests := []struct {
		name string
		rows []Row
		want error
	}{
		{"duplicate key", []Row{ok(1), ok(1)}, ErrDuplicateKeys},
		{"out of order", []Row{ok(2), ok(1)}, ErrRows},
		{"short row", []Row{{Key: 1, Values: []string{"1"}}}, ErrRows},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := compareRows(cols, tt.rows, nil); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}

// TestDiffFindsEveryMutation compares random tables with random changes, at several
// search settings, against the changes that were made.
func TestDiffFindsEveryMutation(t *testing.T) {
	t.Parallel()
	schema := testSchema(t)
	settings := []Options{
		{},                                // defaults: one row comparison for small tables
		{Branching: 2, RowThreshold: 1},   // deepest search
		{Branching: 4, RowThreshold: 16},  // a few levels
		{Branching: 16, RowThreshold: 64}, // wide
	}
	for seed := range uint64(8) {
		r := rand.New(rand.NewPCG(seed, 1))
		recs := genRecords(r, 50+r.IntN(2000), r.Int64N(1e6)-5e5)
		changed, muts := mutate(r, recs, []float64{0, 0.001, 0.02, 0.3}[seed%4])
		for _, opts := range settings {
			left := newMemorySide(t, schema, memRows(t, recs))
			right := newMemorySide(t, schema, memRows(t, changed))
			res, err := Diff(context.Background(), schema, left, right, opts)
			if err != nil {
				t.Fatalf("seed %d, %+v: %v", seed, opts, err)
			}
			checkMutations(t, res, muts)
			if res.Truncated {
				t.Errorf("seed %d, %+v: truncated", seed, opts)
			}
			if !slices.IsSortedFunc(res.Differences, func(a, b Difference) int { return cmpInt64(a.Key, b.Key) }) {
				t.Errorf("seed %d, %+v: differences are not in key order", seed, opts)
			}
			if got := left.queries.Load() + right.queries.Load(); got != res.Stats.Queries {
				t.Errorf("seed %d, %+v: Stats.Queries = %d, sides saw %d", seed, opts, res.Stats.Queries, got)
			}
		}
	}
}

// TestDiffFetchesFewRows checks the point of bisection: a few changes in a large table
// are found by fetching rows only around them.
func TestDiffFetchesFewRows(t *testing.T) {
	t.Parallel()
	schema := testSchema(t)
	r := rand.New(rand.NewPCG(42, 42))
	recs := genRecords(r, 100_000, 0)
	changed := slices.Clone(recs)
	for _, i := range []int{17, 50_000, 99_999} {
		changed[i].label += "!"
		changed[i].labelNull = false
	}
	res, err := Diff(context.Background(), schema,
		newMemorySide(t, schema, memRows(t, recs)), newMemorySide(t, schema, memRows(t, changed)),
		Options{RowThreshold: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Differences) != 3 {
		t.Fatalf("got %d differences, want 3", len(res.Differences))
	}
	if res.Stats.RowsFetched > 3*2*100 {
		t.Errorf("fetched %d rows to find 3 differences", res.Stats.RowsFetched)
	}
	t.Logf("%+v", res.Stats)
}

func TestDiffStopsAtMaxDifferences(t *testing.T) {
	t.Parallel()
	schema := testSchema(t)
	r := rand.New(rand.NewPCG(7, 7))
	recs := genRecords(r, 1000, 0)
	res, err := Diff(context.Background(), schema,
		newMemorySide(t, schema, memRows(t, recs)), newMemorySide(t, schema, nil),
		Options{MaxDifferences: 10, RowThreshold: 8, Branching: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Differences) != 10 || res.Equal() {
		t.Errorf("got %d differences, truncated %v", len(res.Differences), res.Truncated)
	}
}

func TestDiffEdgeCases(t *testing.T) {
	t.Parallel()
	schema := testSchema(t)
	r := rand.New(rand.NewPCG(3, 3))
	recs := genRecords(r, 10, 0)
	tests := []struct {
		name        string
		left, right []record
		want        int
	}{
		{"both empty", nil, nil, 0},
		{"left empty", nil, recs, 10},
		{"right empty", recs, nil, 10},
		{"equal", recs, recs, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res, err := Diff(context.Background(), schema,
				newMemorySide(t, schema, memRows(t, tt.left)), newMemorySide(t, schema, memRows(t, tt.right)),
				Options{RowThreshold: 2, Branching: 2})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Differences) != tt.want || res.Equal() != (tt.want == 0) {
				t.Errorf("got %d differences, want %d", len(res.Differences), tt.want)
			}
		})
	}
}

func TestDiffRejectsDuplicateKeys(t *testing.T) {
	t.Parallel()
	schema := testSchema(t)
	recs := genRecords(rand.New(rand.NewPCG(5, 5)), 3, 0)
	dup := append(slices.Clone(recs), recs[1])
	_, err := Diff(context.Background(), schema,
		newMemorySide(t, schema, memRows(t, recs)), newMemorySide(t, schema, memRows(t, dup)), Options{})
	if !errors.Is(err, ErrDuplicateKeys) {
		t.Errorf("got %v, want %v", err, ErrDuplicateKeys)
	}
}

// failingSide fails every query after the first n.
type failingSide struct {
	Side
	n int
}

var errQuery = errors.New("query failed")

func (f *failingSide) Checksums(ctx context.Context, r Range, splits []int64) ([]Checksum, error) {
	if f.n--; f.n < 0 {
		return nil, errQuery
	}
	return f.Side.Checksums(ctx, r, splits)
}

func TestDiffReturnsQueryErrors(t *testing.T) {
	t.Parallel()
	schema := testSchema(t)
	r := rand.New(rand.NewPCG(9, 9))
	recs := genRecords(r, 500, 0)
	changed, _ := mutate(r, recs, 0.5)
	left := &failingSide{Side: newMemorySide(t, schema, memRows(t, recs)), n: 3}
	_, err := Diff(context.Background(), schema, left, newMemorySide(t, schema, memRows(t, changed)),
		Options{Branching: 2, RowThreshold: 4, Concurrency: 1})
	if !errors.Is(err, errQuery) {
		t.Errorf("got %v, want %v", err, errQuery)
	}
}

func TestDiffHonoursCancellation(t *testing.T) {
	t.Parallel()
	schema := testSchema(t)
	recs := genRecords(rand.New(rand.NewPCG(11, 11)), 100, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	side := newMemorySide(t, schema, memRows(t, recs))
	_, err := Diff(ctx, schema, side, newMemorySide(t, schema, nil), Options{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want %v", err, context.Canceled)
	}
}
