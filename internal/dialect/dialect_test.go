package dialect

import (
	"errors"
	"strings"
	"testing"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

func TestDuckDBQuoteIdent(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"id":         `"id"`,
		"Mixed Case": `"Mixed Case"`,
		`we"ird`:     `"we""ird"`,
		"select":     `"select"`,
	}
	for in, want := range tests {
		if got := (DuckDB{}).QuoteIdent(in); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRowTextReadsColumnsThroughTheMapping(t *testing.T) {
	t.Parallel()
	enc, err := canon.NewEncoder([]canon.Column{{Name: "a", Type: canon.TypeInt}, {Name: "b", Type: canon.TypeInt}})
	if err != nil {
		t.Fatal(err)
	}
	renamed := map[string]string{"a": `"left_a"`, "b": `"left_b"`}
	sql, err := RowText(DuckDB{}, enc, func(c canon.Column) string { return renamed[c.Name] })
	if err != nil {
		t.Fatal(err)
	}
	first, second := strings.Index(sql, `"left_a"`), strings.Index(sql, `"left_b"`)
	if first < 0 || second < first || strings.Contains(sql, `"a"`) {
		t.Errorf("columns not read through the mapping, in schema order:\n%s", sql)
	}
	quoted, err := RowHash(DuckDB{}, enc, Quoted(DuckDB{}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(quoted, `"a"`) || !strings.HasPrefix(quoted, "CAST('0x' || substr(md5(") {
		t.Errorf("unexpected hash expression:\n%s", quoted)
	}
}

func TestDuckDBRejectsUnknownTypes(t *testing.T) {
	t.Parallel()
	for _, typ := range []canon.Type{canon.TypeInvalid, canon.TypeString + 1} {
		if _, err := (DuckDB{}).ValueText(canon.Column{Name: "x", Type: typ}, `"x"`); !errors.Is(err, ErrUnsupported) {
			t.Errorf("ValueText(%v) error = %v, want ErrUnsupported", typ, err)
		}
	}
}

// TestDuckDBUsesTheReferenceConstants checks that the SQL embeds the exact values the Go
// encoder uses, so the two cannot drift apart silently. The conformance test checks the
// results on a real engine.
func TestDuckDBUsesTheReferenceConstants(t *testing.T) {
	t.Parallel()
	abs := tolerance.Tolerance{Abs: 1e-6}
	rel := tolerance.Tolerance{Rel: 1e-9}
	tests := []struct {
		name string
		col  canon.Column
		want []string
	}{
		{"exact float", canon.Column{Type: canon.TypeFloat}, []string{"printf('%.16e'", exactFloatText(0)}},
		{"absolute bucket", canon.Column{Type: canon.TypeFloat, Tolerance: abs}, []string{floatLiteral(abs.AbsWidth()), "4503599627370496"}},
		{"relative bucket", canon.Column{Type: canon.TypeFloat, Tolerance: rel}, []string{floatLiteral(rel.LogWidth()), "'~0~'", "'~-~'"}},
		{"decimal scale", canon.Column{Type: canon.TypeDecimal, Scale: 7}, []string{"DECIMAL(38, 7)"}},
		{"NULL token", canon.Column{Type: canon.TypeString}, []string{`'\N'`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.col.Name = "x"
			sql, err := (DuckDB{}).ValueText(tt.col, `"x"`)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(sql, w) {
					t.Errorf("SQL does not contain %q:\n%s", w, sql)
				}
			}
		})
	}
}
