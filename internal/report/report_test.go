package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/diff"
)

var columns = []canon.Column{
	{Name: "id", Type: canon.TypeInt},
	{Name: "label", Type: canon.TypeString},
	{Name: "score", Type: canon.TypeFloat},
}

func sample() *Report {
	return &Report{
		Left: "duckdb:main.orders", Right: "postgres:public.orders", Columns: columns,
		Elapsed: 1234567 * time.Microsecond,
		Result: &diff.Result{
			Differences: []diff.Difference{
				{Key: 3, Kind: diff.OnlyLeft, Left: []string{"3", `a\|b`, `\N`}},
				{
					Key: 7, Kind: diff.Changed, Columns: []string{"label", "score"},
					Left:  []string{"7", `back\\slash`, "1.0000000000000000e+00"},
					Right: []string{"7", `\N`, "2.0000000000000000e+00"},
				},
				{Key: 9, Kind: diff.OnlyRight, Right: []string{"9", "", "nan"}},
			},
			Stats: diff.Stats{LeftRows: 12_345, RightRows: 12_344, Rounds: 3, Queries: 14, RowsFetched: 2_001},
		},
	}
}

func TestText(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := Text(&b, sample(), -1); err != nil {
		t.Fatal(err)
	}
	want := `status: differs · 12,345 left, 12,344 right rows · 3 differences (1 only left, 1 only right, 1 changed) · 14 queries · 2,001 rows fetched · 1.235s
changed columns: label 1, score 1

only-left  id=3
changed    id=7  label: "back\\slash" → NULL  score: 1.0000000000000000e+00 → 2.0000000000000000e+00
only-right id=9
`
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTextLimitsRows(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := Text(&b, sample(), 1); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(b.String(), "only-left  id=3\n… 2 more\n") {
		t.Errorf("got:\n%s", b.String())
	}
}

func TestStatus(t *testing.T) {
	t.Parallel()
	r := sample()
	if s := r.Status(); s != Differs {
		t.Errorf("with differences: %s", s)
	}
	r.Result.Differences = nil
	if s := r.Status(); s != Equal {
		t.Errorf("without: %s", s)
	}
	r.Result.Unresolved = []diff.Unresolved{{Range: diff.Range{Lo: 1, Hi: 5}, LeftRows: 2, RightRows: 3}}
	if s := r.Status(); s != Incomplete {
		t.Errorf("with unresolved ranges: %s", s)
	}
	var b bytes.Buffer
	if err := Text(&b, r, -1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "undecided: 1 key ranges (5 rows)") {
		t.Errorf("got:\n%s", b.String())
	}
}

func TestJSON(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version     int
		Status      string
		Complete    bool
		Key         string
		Summary     Summary
		Differences []struct {
			Key         int64
			Kind        string
			Columns     []string
			Left, Right map[string]*string
		}
		Unresolved []any
		Stats      struct{ Seconds float64 }
	}
	if err := json.Unmarshal(b.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != 1 || doc.Status != "differs" || !doc.Complete || doc.Key != "id" ||
		doc.Summary.Differences != 3 || doc.Summary.Columns["score"] != 1 || doc.Unresolved == nil {
		t.Errorf("document: %s", b.String())
	}
	d := doc.Differences[1]
	if d.Kind != "changed" || *d.Left["label"] != `back\slash` || d.Right["label"] != nil || *d.Right["score"] != "2.0000000000000000e+00" {
		t.Errorf("changed row: %+v", d)
	}
	if l := doc.Differences[0].Left["label"]; *l != "a|b" || doc.Differences[0].Right != nil {
		t.Errorf("only-left row: %+v", doc.Differences[0])
	}
}

func TestGroup(t *testing.T) {
	t.Parallel()
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", -1234567: "-1,234,567"} {
		if got := group(n); got != want {
			t.Errorf("group(%d) = %q, want %q", n, got, want)
		}
	}
}
