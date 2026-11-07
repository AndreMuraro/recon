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

func TestRowTextNeedsOneExpressionPerColumn(t *testing.T) {
	t.Parallel()
	enc, err := canon.NewEncoder([]canon.Column{{Name: "a", Type: canon.TypeInt}, {Name: "b", Type: canon.TypeInt}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RowText(DuckDB{}, enc, []string{`"a"`}); err == nil {
		t.Error("RowText accepted one expression for two columns")
	}
	if _, err := RowHash(DuckDB{}, enc, []string{`"a"`, `"b"`, `"c"`}); err == nil {
		t.Error("RowHash accepted three expressions for two columns")
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
