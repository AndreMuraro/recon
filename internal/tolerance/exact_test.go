package tolerance

import (
	"errors"
	"math"
	"testing"
)

// The expected texts were computed independently with Python's math.frexp.
func TestAppendExact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		x    float64
		want string
	}{
		{0.1, "7205759403792794p-56"},
		{-0.1, "-7205759403792794p-56"},
		{1, "4503599627370496p-52"},
		{100000, "6871947673600000p-36"},
		{1.0 / 3, "6004799503160661p-54"},
		{30405.13256, "8357699198447664p-38"},
		{-3.0517578125e-05, "-4503599627370496p-67"},
		{0x1p-1022, "4503599627370496p-1074"},              // smallest normal
		{2.225073858507201e-308, "4503599627370495p-1074"}, // largest subnormal
		{5e-324, "1p-1074"},                                // smallest subnormal
		{math.MaxFloat64, "9007199254740991p971"},
		{0, "0"},
		{math.Copysign(0, -1), "0"},
		{math.NaN(), "nan"},
		{math.Inf(1), "inf"},
		{math.Inf(-1), "-inf"},
	}
	for _, tt := range tests {
		if got := string(AppendExact(nil, tt.x)); got != tt.want {
			t.Errorf("AppendExact(%v) = %s, want %s", tt.x, got, tt.want)
		}
	}
}

func TestParseExactRejectsNonCanonicalText(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"", "1.5", "1e5", "NaN", "-0", "+1p0", "1p", "p3",
		"2251799813685248p-51", // 0.5 is 4503599627370496p-53: not the full significand
		"4503599627370496p-1075", "1p-1075", "9007199254740992p971",
	} {
		if _, err := ParseExact(s); !errors.Is(err, ErrExactText) {
			t.Errorf("ParseExact(%q): got %v, want %v", s, err, ErrExactText)
		}
	}
}

// FuzzExactRoundTrip checks that every float's text reads back as the same float, so the
// text is injective: two floats never share one.
func FuzzExactRoundTrip(f *testing.F) {
	for _, x := range []float64{0, 0.1, -1, 5e-324, math.MaxFloat64, 0x1p-1022, math.Inf(-1)} {
		f.Add(math.Float64bits(x))
	}
	f.Fuzz(func(t *testing.T, bits uint64) {
		x := math.Float64frombits(bits)
		s := string(AppendExact(nil, x))
		y, err := ParseExact(s)
		if err != nil {
			t.Fatalf("%v (%s): %v", x, s, err)
		}
		if math.IsNaN(x) {
			if !math.IsNaN(y) {
				t.Fatalf("%s read back as %v", s, y)
			}
			return
		}
		if x == 0 {
			x = 0 // negative zero is zero
		}
		if math.Float64bits(y) != math.Float64bits(x) {
			t.Fatalf("%v (%s) read back as %v", x, s, y)
		}
	})
}
