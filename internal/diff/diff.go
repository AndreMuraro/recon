// Package diff finds the rows that differ between two tables, by bisecting key ranges
// whose checksums disagree and comparing rows only where checksums cannot decide
// (ADR 0001, ADR 0004).
//
// The engines are reached through [Side]; [SQLSide] implements it with a [Querier] and a
// SQL dialect. This package never sees driver types: every value arrives as the canonical
// text the dialect computed.
package diff

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/AndreMuraro/recon/internal/canon"
)

// ErrDuplicateKeys reports a side whose key is not unique.
var ErrDuplicateKeys = errors.New("duplicate keys")

// Range is an inclusive range of keys, lo <= key <= hi. Inclusive bounds can express the
// whole int64 range.
type Range struct{ Lo, Hi int64 }

// Bounds summarises one side.
type Bounds struct {
	Rows, Keys int64 // Keys counts distinct keys
	Min, Max   int64 // meaningful only when Rows > 0
}

// Checksum is the row count and the exact sum of the 60-bit row checksums of a range, in
// base 10. Two ranges agree when both are equal.
type Checksum struct {
	Rows int64
	Sum  string
}

// Row is one row as comparison text: the key's text first, then each compared column,
// in schema order.
type Row struct {
	Key    int64
	Values []string
}

// Side is one table on one engine.
type Side interface {
	Bounds(ctx context.Context) (Bounds, error)
	// Checksums returns one Checksum per child of r, split before each of splits;
	// empty children have zero rows.
	Checksums(ctx context.Context, r Range, splits []int64) ([]Checksum, error)
	// Rows returns the rows of r in key order.
	Rows(ctx context.Context, r Range) ([]Row, error)
}

// Options tune the search. The zero value uses the defaults.
type Options struct {
	// Branching is the number of children a disagreeing range is split into (default 16).
	Branching int
	// RowThreshold is the row count at or below which a range is fetched rather than
	// split (default 1,000).
	RowThreshold int64
	// MaxDifferences stops the search once this many differences are found (default
	// 10,000). Ranges not yet searched are reported as unresolved, and differences found
	// beyond the limit are dropped and the result marked truncated.
	MaxDifferences int
	// MaxRowsFetched caps the rows fetched for comparison, across both sides (default
	// 100,000). Ranges that would exceed it are reported as unresolved, not fetched.
	MaxRowsFetched int64
	// Concurrency is the number of ranges searched at once, each querying both sides in
	// parallel (default 4).
	Concurrency int
}

func (o Options) withDefaults() Options {
	if o.Branching < 2 {
		o.Branching = 16
	}
	if o.RowThreshold < 1 {
		o.RowThreshold = 1_000
	}
	if o.MaxDifferences < 1 {
		o.MaxDifferences = 10_000
	}
	if o.MaxRowsFetched < 1 {
		o.MaxRowsFetched = 100_000
	}
	if o.Concurrency < 1 {
		o.Concurrency = 4
	}
	return o
}

// Result is the outcome of a comparison.
type Result struct {
	Differences []Difference // in key order
	// Unresolved are key ranges whose checksums disagree but whose rows were not
	// compared, because a limit was reached; in key order. Each holds at least one
	// difference, unless only floats within tolerance changed bucket.
	Unresolved []Unresolved
	// Truncated reports that differences beyond Options.MaxDifferences were dropped.
	Truncated bool
	Stats     Stats
}

// Unresolved is a key range left undecided, with its row count on each side.
type Unresolved struct {
	Range
	LeftRows, RightRows int64
}

// Equal reports whether the tables were proven equal: no differences, and nothing left
// undecided.
func (r *Result) Equal() bool {
	return len(r.Differences) == 0 && len(r.Unresolved) == 0 && !r.Truncated
}

// Complete reports whether every difference was found.
func (r *Result) Complete() bool { return len(r.Unresolved) == 0 && !r.Truncated }

// Stats count the work a comparison did.
type Stats struct {
	LeftRows, RightRows int64
	Rounds              int   // levels of bisection
	Queries             int64 // across both sides
	RowsFetched         int64 // across both sides
}

// Diff compares left and right, two sides with the same schema: the key first, then the
// compared columns.
func Diff(ctx context.Context, schema *canon.Encoder, left, right Side, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	d := &differ{
		cols:  schema.Columns(),
		left:  left,
		right: right,
		opts:  opts,
	}
	d.budget.Store(opts.MaxRowsFetched)

	bl, br, err := both(ctx, left.Bounds, right.Bounds)
	d.queries.Add(2)
	if err != nil {
		return nil, err
	}
	for _, s := range []struct {
		name string
		b    Bounds
	}{{"left", bl}, {"right", br}} {
		if s.b.Rows != s.b.Keys {
			return nil, fmt.Errorf("%w: the %s side has %d rows for %d distinct keys",
				ErrDuplicateKeys, s.name, s.b.Rows, s.b.Keys)
		}
	}
	res := &Result{Stats: Stats{LeftRows: bl.Rows, RightRows: br.Rows}}
	if bl.Rows == 0 && br.Rows == 0 {
		res.Stats.Queries = d.queries.Load()
		return res, nil
	}

	whole := Range{Lo: bl.Min, Hi: bl.Max}
	switch {
	case bl.Rows == 0:
		whole = Range{Lo: br.Min, Hi: br.Max}
	case br.Rows > 0:
		whole = Range{Lo: min(bl.Min, br.Min), Hi: max(bl.Max, br.Max)}
	}
	if err := d.search(ctx, pending{r: whole, left: bl.Rows, right: br.Rows}, &res.Stats); err != nil {
		return nil, err
	}

	res.Differences = d.diffs
	slices.SortFunc(res.Differences, func(a, b Difference) int { return cmpInt64(a.Key, b.Key) })
	if len(res.Differences) > opts.MaxDifferences {
		res.Differences, res.Truncated = res.Differences[:opts.MaxDifferences], true
	}
	res.Unresolved = d.unresolved
	slices.SortFunc(res.Unresolved, func(a, b Unresolved) int { return cmpInt64(a.Lo, b.Lo) })
	res.Stats.Queries = d.queries.Load()
	res.Stats.RowsFetched = d.fetched.Load()
	return res, nil
}

// pending is a range that disagrees, with its row count on each side.
type pending struct {
	r           Range
	left, right int64
}

type differ struct {
	cols        []canon.Column
	left, right Side
	opts        Options

	queries, fetched atomic.Int64
	budget           atomic.Int64 // rows that may still be fetched

	mu         sync.Mutex
	diffs      []Difference
	unresolved []Unresolved
}

// search bisects level by level until no range disagrees. Once enough differences are
// found, the ranges still pending are left unresolved.
func (d *differ) search(ctx context.Context, root pending, stats *Stats) error {
	level := []pending{root}
	for len(level) > 0 {
		if d.full() {
			for _, p := range level {
				d.leave(p)
			}
			return nil
		}
		stats.Rounds++
		next, err := d.step(ctx, level)
		if err != nil {
			return err
		}
		level = next
	}
	return nil
}

// step resolves every range of one level: small ones are compared row by row, larger
// ones are split, and the children that disagree are returned.
func (d *differ) step(ctx context.Context, level []pending) ([]pending, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		next     []pending
		firstErr error
		wg       sync.WaitGroup
		sem      = make(chan struct{}, d.opts.Concurrency)
	)
	for _, p := range level {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			children, err := d.resolve(ctx, p)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			next = append(next, children...)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(next, func(a, b pending) int { return cmpInt64(a.r.Lo, b.r.Lo) })
	return next, nil
}

// resolve compares a small range row by row, or splits a larger one and returns the
// children whose checksums disagree. Once the row budget is spent, ranges are left
// unresolved instead: splitting them further would not lead to a fetch.
func (d *differ) resolve(ctx context.Context, p pending) ([]pending, error) {
	if d.budget.Load() <= 0 {
		d.leave(p)
		return nil, nil
	}
	splits := p.r.split(d.opts.Branching)
	if max(p.left, p.right) <= d.opts.RowThreshold || len(splits) == 0 {
		if !d.reserve(p.left + p.right) {
			d.leave(p)
			return nil, nil
		}
		return nil, d.compareRows(ctx, p.r)
	}
	left, right, err := both(ctx,
		func(ctx context.Context) ([]Checksum, error) { return d.left.Checksums(ctx, p.r, splits) },
		func(ctx context.Context) ([]Checksum, error) { return d.right.Checksums(ctx, p.r, splits) })
	d.queries.Add(2)
	if err != nil {
		return nil, err
	}
	if len(left) != len(splits)+1 || len(right) != len(splits)+1 {
		return nil, fmt.Errorf("checksums: got %d and %d children, want %d", len(left), len(right), len(splits)+1)
	}
	var out []pending
	for i := range left {
		if left[i] != right[i] {
			out = append(out, pending{r: p.r.child(splits, i), left: left[i].Rows, right: right[i].Rows})
		}
	}
	return out, nil
}

func (d *differ) compareRows(ctx context.Context, r Range) error {
	left, right, err := both(ctx,
		func(ctx context.Context) ([]Row, error) { return d.left.Rows(ctx, r) },
		func(ctx context.Context) ([]Row, error) { return d.right.Rows(ctx, r) })
	d.queries.Add(2)
	if err != nil {
		return err
	}
	d.fetched.Add(int64(len(left) + len(right)))
	found, err := compareRows(d.cols, left, right)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.diffs = append(d.diffs, found...)
	d.mu.Unlock()
	return nil
}

// reserve takes n rows from the fetch budget, or reports that they don't fit. Row counts
// come from the checksums, so a range fetches exactly what it reserved.
func (d *differ) reserve(n int64) bool {
	for {
		left := d.budget.Load()
		if n > left {
			// Nothing smaller will fit for long either: close the budget, so that the
			// remaining ranges are left whole instead of split.
			d.budget.CompareAndSwap(left, 0)
			return false
		}
		if d.budget.CompareAndSwap(left, left-n) {
			return true
		}
	}
}

// leave records p as unresolved.
func (d *differ) leave(p pending) {
	d.mu.Lock()
	d.unresolved = append(d.unresolved, Unresolved{Range: p.r, LeftRows: p.left, RightRows: p.right})
	d.mu.Unlock()
}

func (d *differ) full() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.diffs) >= d.opts.MaxDifferences
}

// split returns the first key of each child after the first, dividing r into at most n
// children of near-equal width: child i starts at lo + floor(i*size/c). The arithmetic is
// exact for any range, including the full int64 range of 2^64 keys.
func (r Range) split(n int) []int64 {
	span := uint64(r.Hi) - uint64(r.Lo) //nolint:gosec // two's-complement difference of int64s, by design
	// size = span + 1 as a 128-bit number (sizeHi is 1 only for the full range).
	sizeLo, sizeHi := bits.Add64(span, 1, 0)
	c := uint64(n) //nolint:gosec // n is a small positive branching factor
	if sizeHi == 0 && sizeLo < c {
		c = sizeLo
	}
	if c < 2 {
		return nil
	}
	splits := make([]int64, 0, c-1)
	for i := uint64(1); i < c; i++ {
		hi, lo := bits.Mul64(sizeLo, i)
		hi += sizeHi * i
		offset, _ := bits.Div64(hi, lo, c)                  // hi < c because i < c
		splits = append(splits, int64(uint64(r.Lo)+offset)) //nolint:gosec // wraps back into int64 by design
	}
	return splits
}

// child returns child i of r for the given splits.
func (r Range) child(splits []int64, i int) Range {
	c := r
	if i > 0 {
		c.Lo = splits[i-1]
	}
	if i < len(splits) {
		c.Hi = splits[i] - 1
	}
	return c
}

// both runs f and g concurrently and returns both results, or the first error.
func both[A, B any](ctx context.Context, f func(context.Context) (A, error), g func(context.Context) (B, error)) (A, B, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		a          A
		errA, errB error
		done       = make(chan struct{})
	)
	go func() {
		defer close(done)
		if a, errA = f(ctx); errA != nil {
			cancel()
		}
	}()
	b, errB := g(ctx)
	if errB != nil {
		cancel()
	}
	<-done
	return a, b, errors.Join(errA, errB)
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
