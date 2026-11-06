package canon

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/AndreMuraro/recon/internal/tolerance"
)

// sampleColumns covers every type; the golden values below were computed independently
// with Python's hashlib.
var sampleColumns = []Column{
	{Name: "id", Type: TypeInt},
	{Name: "amount", Type: TypeDecimal, Scale: 2},
	{Name: "tce", Type: TypeFloat, Tolerance: tolerance.Tolerance{Abs: 1e-6}},
	{Name: "ok", Type: TypeBool},
	{Name: "day", Type: TypeDate},
	{Name: "at", Type: TypeTimestamp},
	{Name: "note", Type: TypeString},
}

func sampleRow(t *testing.T) []Value {
	t.Helper()
	at := time.Date(2026, time.November, 6, 12, 0, 0, 1000, time.UTC)
	return []Value{
		Int(42), mustDecimal(t, "12.5"), Float(30405.13256), Bool(true),
		check(t)(Date(at)), check(t)(Timestamp(at)), String(`a|b\c`),
	}
}

// check fails the test on err and returns v, for constructors that can fail.
func check(t *testing.T) func(Value, error) Value {
	t.Helper()
	return func(v Value, err error) Value {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func mustDecimal(t *testing.T, s string) Value {
	t.Helper()
	return check(t)(Decimal(s))
}

func mustEncoder(t *testing.T, cols ...Column) *Encoder {
	t.Helper()
	e, err := NewEncoder(cols)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestGoldenRow(t *testing.T) {
	t.Parallel()
	e := mustEncoder(t, sampleColumns...)
	row := sampleRow(t)

	got, err := e.AppendRow(nil, row)
	if err != nil {
		t.Fatal(err)
	}
	const want = `42|12.50|~60810265120|t|2026-11-06|1793966400000001|a\|b\\c`
	if string(got) != want {
		t.Errorf("AppendRow\n got %s\nwant %s", got, want)
	}

	hash, err := e.Hash60(row)
	if err != nil {
		t.Fatal(err)
	}
	const wantHash = 1120900644797120881
	if hash != wantHash {
		t.Errorf("Hash60 = %d, want %d", hash, uint64(wantHash))
	}
	if hash >= 1<<60 {
		t.Errorf("Hash60 = %d does not fit in 60 bits", hash)
	}
}

func TestGoldenNulls(t *testing.T) {
	t.Parallel()
	e := mustEncoder(t, sampleColumns...)
	row := make([]Value, len(sampleColumns))
	for i, c := range sampleColumns {
		row[i] = Null(c.Type)
	}
	got, err := e.AppendRow(nil, row)
	if err != nil {
		t.Fatal(err)
	}
	if want := `\N|\N|\N|\N|\N|\N|\N`; string(got) != want {
		t.Errorf("AppendRow = %s, want %s", got, want)
	}
	if hash, _ := e.Hash60(row); hash != 682212060942403482 {
		t.Errorf("Hash60 = %d, want 682212060942403482", hash)
	}
}

func TestEncodeValues(t *testing.T) {
	t.Parallel()
	ts := func(s string) Value {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return check(t)(Timestamp(parsed))
	}
	tests := []struct {
		name string
		col  Column
		v    Value
		want string
	}{
		{"negative int", Column{Type: TypeInt}, Int(-7), "-7"},
		{"decimal padded to scale", Column{Type: TypeDecimal, Scale: 4}, mustDecimal(t, "1.5"), "1.5000"},
		{"decimal leading zeros dropped", Column{Type: TypeDecimal, Scale: 2}, mustDecimal(t, "007.10"), "7.10"},
		{"decimal below one", Column{Type: TypeDecimal, Scale: 3}, mustDecimal(t, "-0.05"), "-0.050"},
		{"decimal negative zero", Column{Type: TypeDecimal, Scale: 2}, mustDecimal(t, "-0.00"), "0.00"},
		{"decimal integer scale", Column{Type: TypeDecimal}, mustDecimal(t, "120"), "120"},
		{"exact float", Column{Type: TypeFloat}, Float(0.1), "0.1"},
		{"exact float exponent", Column{Type: TypeFloat}, Float(1e21), "1e+21"},
		{"exact negative zero", Column{Type: TypeFloat}, Float(math.Copysign(0, -1)), "0"},
		{"exact NaN", Column{Type: TypeFloat}, Float(math.NaN()), "nan"},
		{"float bucket", Column{Type: TypeFloat, Tolerance: tolerance.Tolerance{Abs: 0.5}}, Float(1.3), "~5"},
		{"false", Column{Type: TypeBool}, Bool(false), "f"},
		{"timestamp before 1970", Column{Type: TypeTimestamp}, ts("1969-12-31T23:59:59.999999Z"), "-1"},
		{"timestamp offset is an instant", Column{Type: TypeTimestamp}, ts("1970-01-01T01:00:00+01:00"), "0"},
		{"string untouched", Column{Type: TypeString}, String(" Ação "), " Ação "},
		{"string that looks like NULL", Column{Type: TypeString}, String(`\N`), `\\N`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.col.Name = "c"
			got, err := mustEncoder(t, tt.col).AppendRow(nil, []Value{tt.v})
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDateUsesTheCalendarDayOfItsLocation(t *testing.T) {
	t.Parallel()
	sp := time.FixedZone("UTC-3", -3*60*60)
	late := time.Date(2026, time.November, 6, 23, 30, 0, 0, sp) // already the 7th in UTC
	e := mustEncoder(t, Column{Name: "d", Type: TypeDate})
	got, err := e.AppendRow(nil, []Value{check(t)(Date(late))})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "2026-11-06" {
		t.Errorf("got %s, want 2026-11-06", got)
	}
}

func TestConstructorErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"decimal exponent", second(Decimal("1e5")), ErrSyntax},
		{"decimal plus sign", second(Decimal("+1")), ErrSyntax},
		{"decimal empty", second(Decimal("")), ErrSyntax},
		{"decimal bare point", second(Decimal("1.")), ErrSyntax},
		{"decimal missing whole part", second(Decimal(".5")), ErrSyntax},
		{"decimal thousands separator", second(Decimal("1,000")), ErrSyntax},
		{"decimal too many places", second(Decimal("0." + strings.Repeat("1", MaxScale+1))), ErrRange},
		{"date year 0", second(Date(time.Date(0, 12, 31, 0, 0, 0, 0, time.UTC))), ErrRange},
		{"date year 10000", second(Date(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))), ErrRange},
		{"timestamp nanoseconds", second(Timestamp(time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC))), ErrRange},
		{"timestamp year 10000", second(Timestamp(time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))), ErrRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if !errors.Is(tt.err, tt.want) {
				t.Errorf("error = %v, want %v", tt.err, tt.want)
			}
		})
	}
}

func second[T any](_ T, err error) error { return err }

func TestSchemaErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cols []Column
	}{
		{"no columns", nil},
		{"unnamed", []Column{{Type: TypeInt}}},
		{"duplicate", []Column{{Name: "a", Type: TypeInt}, {Name: "a", Type: TypeString}}},
		{"invalid type", []Column{{Name: "a"}}},
		{"unknown type", []Column{{Name: "a", Type: TypeString + 1}}},
		{"negative scale", []Column{{Name: "a", Type: TypeDecimal, Scale: -1}}},
		{"scale too large", []Column{{Name: "a", Type: TypeDecimal, Scale: MaxScale + 1}}},
		{"scale on an int", []Column{{Name: "a", Type: TypeInt, Scale: 2}}},
		{"tolerance on a decimal", []Column{{Name: "a", Type: TypeDecimal, Tolerance: tolerance.Tolerance{Abs: 1}}}},
		{"invalid tolerance", []Column{{Name: "a", Type: TypeFloat, Tolerance: tolerance.Tolerance{Rel: 2}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewEncoder(tt.cols); !errors.Is(err, ErrSchema) {
				t.Errorf("NewEncoder error = %v, want ErrSchema", err)
			}
		})
	}
}

func TestMismatchErrors(t *testing.T) {
	t.Parallel()
	e := mustEncoder(t, Column{Name: "n", Type: TypeInt}, Column{Name: "d", Type: TypeDecimal, Scale: 1})
	tests := []struct {
		name string
		row  []Value
	}{
		{"too few values", []Value{Int(1)}},
		{"wrong type", []Value{String("1"), mustDecimal(t, "1")}},
		{"NULL of the wrong type", []Value{Null(TypeString), mustDecimal(t, "1")}},
		{"zero Value", []Value{{}, mustDecimal(t, "1")}},
		{"more decimal places than the column", []Value{Int(1), mustDecimal(t, "1.25")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := e.AppendRow(nil, tt.row); !errors.Is(err, ErrMismatch) {
				t.Errorf("AppendRow error = %v, want ErrMismatch", err)
			}
		})
	}
}

func TestNewEncoderCopiesItsSchema(t *testing.T) {
	t.Parallel()
	cols := []Column{{Name: "a", Type: TypeInt}}
	e := mustEncoder(t, cols...)
	cols[0].Type = TypeString
	e.Columns()[0].Name = "changed"
	if got := e.Columns()[0]; got.Name != "a" || got.Type != TypeInt {
		t.Errorf("schema changed through a shared slice: %+v", got)
	}
}

// decodeStrings is the inverse of the canonical form for string columns: split on
// unescaped separators and remove escapes. ok is false for malformed input.
func decodeStrings(enc string) (fields []*string, ok bool) {
	var cur strings.Builder
	null, escaped := false, false
	flush := func() {
		if null {
			fields = append(fields, nil)
		} else {
			s := cur.String()
			fields = append(fields, &s)
		}
		cur.Reset()
		null = false
	}
	for i := range len(enc) {
		b := enc[i]
		switch {
		case escaped:
			escaped = false
			switch b {
			case escape, sep:
				cur.WriteByte(b)
			case 'N':
				if cur.Len() != 0 || (i+1 < len(enc) && enc[i+1] != sep) {
					return nil, false
				}
				null = true
			default:
				return nil, false
			}
		case b == escape:
			escaped = true
		case b == sep:
			flush()
		default:
			if null {
				return nil, false
			}
			cur.WriteByte(b)
		}
	}
	if escaped {
		return nil, false
	}
	flush()
	return fields, true
}

// FuzzStringRowsRoundTrip shows the encoding is injective for the only type that needs
// escaping: every encoded row decodes back to exactly the values it came from, so two
// different rows can never encode the same.
func FuzzStringRowsRoundTrip(f *testing.F) {
	f.Add("a", "b", false, false)
	f.Add(`\N`, "", false, true)
	f.Add(`a|b\c`, `\`, true, false)
	f.Add("|", `\\|`, false, false)
	e, err := NewEncoder([]Column{{Name: "x", Type: TypeString}, {Name: "y", Type: TypeString}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, x, y string, xNull, yNull bool) {
		in := []*string{&x, &y}
		row := []Value{String(x), String(y)}
		if xNull {
			in[0], row[0] = nil, Null(TypeString)
		}
		if yNull {
			in[1], row[1] = nil, Null(TypeString)
		}
		enc, err := e.AppendRow(nil, row)
		if err != nil {
			t.Fatal(err)
		}
		out, ok := decodeStrings(string(enc))
		if !ok || len(out) != len(in) {
			t.Fatalf("encoding %q does not decode into %d fields", enc, len(in))
		}
		for i := range in {
			if (in[i] == nil) != (out[i] == nil) || (in[i] != nil && *in[i] != *out[i]) {
				t.Fatalf("field %d: %q encoded as %q and decoded differently", i, row[i].s, enc)
			}
		}
	})
}

func FuzzDecimalCanonicalForm(f *testing.F) {
	f.Add("12.5", int32(3))
	f.Add("-0.00", int32(2))
	f.Add("000", int32(0))
	f.Fuzz(func(t *testing.T, s string, scale int32) {
		v, err := Decimal(s)
		if err != nil || scale < v.sc || scale > MaxScale {
			t.Skip()
		}
		e, err := NewEncoder([]Column{{Name: "d", Type: TypeDecimal, Scale: scale}})
		if err != nil {
			t.Fatal(err)
		}
		enc, err := e.AppendRow(nil, []Value{v})
		if err != nil {
			t.Fatal(err)
		}
		// The canonical form parses back to the same decimal, has exactly the column's
		// scale, no superfluous leading zeros and no negative zero.
		text := string(enc)
		back, err := Decimal(text)
		if err != nil {
			t.Fatalf("canonical %q does not parse: %v", text, err)
		}
		if reenc, _ := e.AppendRow(nil, []Value{back}); string(reenc) != text {
			t.Fatalf("%q is not stable: re-encodes as %q", text, reenc)
		}
		whole, frac, _ := strings.Cut(strings.TrimPrefix(text, "-"), ".")
		if len(frac) != int(scale) || (len(whole) > 1 && whole[0] == '0') {
			t.Fatalf("%q (from %q at scale %d) is not canonical", text, s, scale)
		}
		if strings.HasPrefix(text, "-") && strings.Trim(whole+frac, "0") == "" {
			t.Fatalf("%q is a negative zero", text)
		}
	})
}
