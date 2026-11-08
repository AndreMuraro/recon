package diff

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/dialect"
)

// fakeQuerier returns a fixed result.
type fakeQuerier [][]string

func (f fakeQuerier) Query(context.Context, string) ([][]string, error) { return f, nil }

func fakeSide(t *testing.T, result [][]string) *SQLSide {
	t.Helper()
	enc, err := canon.NewEncoder([]canon.Column{
		{Name: "id", Type: canon.TypeInt},
		{Name: "v", Type: canon.TypeString},
	})
	if err != nil {
		t.Fatal(err)
	}
	seg, err := dialect.NewSegmenter(dialect.DuckDB{}, []string{"t"}, enc, dialect.Quoted(dialect.DuckDB{}))
	if err != nil {
		t.Fatal(err)
	}
	return NewSQLSide(fakeQuerier(result), seg)
}

func TestSQLSideReadsResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	b, err := fakeSide(t, [][]string{{"0", "0", "NULL", "NULL"}}).Bounds(ctx)
	if err != nil || b != (Bounds{}) {
		t.Errorf("empty bounds = %+v, %v", b, err)
	}
	sums, err := fakeSide(t, [][]string{{"2", "5", "123"}, {"0", "1", "7"}}).Checksums(ctx, Range{0, 99}, []int64{10, 20})
	if want := []Checksum{{1, "7"}, {}, {5, "123"}}; err != nil || !slices.Equal(sums, want) {
		t.Errorf("checksums = %v, %v; want %v", sums, err, want)
	}
	rows, err := fakeSide(t, [][]string{{"3", "c"}, {"-10", "a"}, {"2", "b"}}).Rows(ctx, Range{-10, 3})
	if err != nil || len(rows) != 3 || rows[0].Key != -10 || rows[2].Values[1] != "c" {
		t.Errorf("rows = %+v, %v; want them in key order", rows, err)
	}
}

func TestSQLSideRejectsMalformedResults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tests := []struct {
		name string
		run  func(*SQLSide) error
		rows [][]string
	}{
		{"bounds width", func(s *SQLSide) error { _, err := s.Bounds(ctx); return err }, [][]string{{"1"}}},
		{"bounds rows", func(s *SQLSide) error { _, err := s.Bounds(ctx); return err }, nil},
		{"bounds text", func(s *SQLSide) error { _, err := s.Bounds(ctx); return err }, [][]string{{"x", "1", "1", "1"}}},
		{"child out of range", func(s *SQLSide) error { _, err := s.Checksums(ctx, Range{0, 9}, []int64{5}); return err }, [][]string{{"2", "1", "1"}}},
		{"child twice", func(s *SQLSide) error { _, err := s.Checksums(ctx, Range{0, 9}, []int64{5}); return err }, [][]string{{"0", "1", "1"}, {"0", "1", "1"}}},
		{"row key", func(s *SQLSide) error { _, err := s.Rows(ctx, Range{0, 9}); return err }, [][]string{{"k", "v"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.run(fakeSide(t, tt.rows)); !errors.Is(err, ErrResult) {
				t.Errorf("got %v, want %v", err, ErrResult)
			}
		})
	}
}
