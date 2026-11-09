package dialect

import (
	"errors"
	"strings"
	"testing"

	"github.com/AndreMuraro/recon/internal/canon"
	"github.com/AndreMuraro/recon/internal/tolerance"
)

// dialects lists every dialect, for the tests that apply to all of them.
var dialects = []Dialect{DuckDB{}, Postgres{}, Snowflake{}, Databricks{}}

func TestQuoteIdent(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"id":         `"id"`,
		"Mixed Case": `"Mixed Case"`,
		`we"ird`:     `"we""ird"`,
		"select":     `"select"`,
	}
	for _, d := range []Dialect{DuckDB{}, Postgres{}, Snowflake{}} {
		for in, want := range tests {
			if got := d.QuoteIdent(in); got != want {
				t.Errorf("%s: QuoteIdent(%q) = %s, want %s", d.Name(), in, got, want)
			}
		}
	}
	if got := (Databricks{}).QuoteIdent("we`ird name"); got != "`we``ird name`" {
		t.Errorf("databricks: QuoteIdent = %s", got)
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

func TestRejectsUnknownTypes(t *testing.T) {
	t.Parallel()
	for _, d := range dialects {
		for _, typ := range []canon.Type{canon.TypeInvalid, canon.TypeString + 1} {
			if _, err := d.ValueText(canon.Column{Name: "x", Type: typ}, `"x"`); !errors.Is(err, ErrUnsupported) {
				t.Errorf("%s: ValueText(%v) error = %v, want ErrUnsupported", d.Name(), typ, err)
			}
		}
	}
}

// TestUsesTheReferenceConstants checks that each dialect's SQL embeds the exact values the
// Go encoder uses, so the two cannot drift apart silently. The conformance tests check the
// results on real engines.
func TestUsesTheReferenceConstants(t *testing.T) {
	t.Parallel()
	abs := tolerance.Tolerance{Abs: 1e-6}
	rel := tolerance.Tolerance{Rel: 1e-9}
	nullToken := map[string]string{"duckdb": `'\N'`, "postgres": `E'\\N'`, "snowflake": `'\\N'`, "databricks": `'\\N'`}
	tests := []struct {
		name string
		col  canon.Column
		want func(Dialect) []string
	}{
		{"exact float", canon.Column{Type: canon.TypeFloat}, func(Dialect) []string {
			// The bounds of the exponent and significand in tolerance.AppendExact.
			return []string{"THEN '0'", "'p'", "-1022", "- 52", "1023"}
		}},
		{"absolute bucket", canon.Column{Type: canon.TypeFloat, Tolerance: abs}, func(Dialect) []string {
			return []string{floatLiteral(abs.AbsWidth()), floatLiteral(abs.AbsLimit())}
		}},
		{"relative bucket", canon.Column{Type: canon.TypeFloat, Tolerance: rel}, func(Dialect) []string {
			return []string{floatLiteral(rel.LogWidth()), "'~0~'", "'~-~'"}
		}},
		{"decimal scale", canon.Column{Type: canon.TypeDecimal, Scale: 7}, func(Dialect) []string {
			return []string{"(38, 7)"}
		}},
		{"NULL token", canon.Column{Type: canon.TypeString}, func(d Dialect) []string {
			return []string{nullToken[d.Name()]}
		}},
	}
	for _, d := range dialects {
		for _, tt := range tests {
			t.Run(d.Name()+"/"+tt.name, func(t *testing.T) {
				t.Parallel()
				col := tt.col
				col.Name = "x"
				sql, err := d.ValueText(col, `"x"`)
				if err != nil {
					t.Fatal(err)
				}
				for _, w := range tt.want(d) {
					if !strings.Contains(sql, w) {
						t.Errorf("SQL does not contain %q:\n%s", w, sql)
					}
				}
			})
		}
	}
}

func TestPostgresHash60(t *testing.T) {
	t.Parallel()
	if got, want := (Postgres{}).Hash60("t"), "CAST(CAST('x' || substr(md5(t), 1, 15) AS bit(60)) AS bigint)"; got != want {
		t.Errorf("Hash60 = %s, want %s", got, want)
	}
}
