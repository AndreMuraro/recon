package dialect

import (
	"errors"
	"strings"
	"testing"

	"github.com/AndreMuraro/recon/internal/canon"
)

func TestSegmenterRejectsNonIntegerKeys(t *testing.T) {
	t.Parallel()
	enc, err := canon.NewEncoder([]canon.Column{{Name: "code", Type: canon.TypeString}})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dialects {
		if _, err := NewSegmenter(d, []string{"t"}, enc, Quoted(d)); !errors.Is(err, ErrKey) {
			t.Errorf("%s: got %v, want %v", d.Name(), err, ErrKey)
		}
	}
}

// TestSegmentQueriesHaveNoOrderBy guards ADR 0004: in Postgres, ORDER BY "id" over the
// rows query bound to the key's text column and sorted keys as strings.
func TestSegmentQueriesHaveNoOrderBy(t *testing.T) {
	t.Parallel()
	enc, err := canon.NewEncoder([]canon.Column{
		{Name: "id", Type: canon.TypeInt},
		{Name: "v", Type: canon.TypeString},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dialects {
		seg, err := NewSegmenter(d, []string{"s", "t"}, enc, Quoted(d))
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range []string{seg.BoundsSQL(), seg.ChecksumsSQL(-5, 5, []int64{0}), seg.RowsSQL(-5, 5)} {
			if strings.Contains(strings.ToUpper(q), "ORDER BY") {
				t.Errorf("%s: %s", d.Name(), q)
			}
			if !strings.Contains(q, "FROM "+d.QuoteIdent("s")+"."+d.QuoteIdent("t")) {
				t.Errorf("%s: table not quoted part by part: %s", d.Name(), q)
			}
		}
		if seg.Columns() != 2 {
			t.Errorf("%s: Columns() = %d, want 2", d.Name(), seg.Columns())
		}
	}
}
