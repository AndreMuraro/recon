package tolerance

import (
	"errors"
	"math"
	"math/rand/v2"
	"testing"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		tol  Tolerance
		ok   bool
	}{
		{"exact", Tolerance{}, true},
		{"absolute", Tolerance{Abs: 1e-6}, true},
		{"relative", Tolerance{Rel: 1e-9}, true},
		{"both", Tolerance{Abs: 1e-6, Rel: 1e-9}, true},
		{"negative absolute", Tolerance{Abs: -1}, false},
		{"infinite absolute", Tolerance{Abs: math.Inf(1)}, false},
		{"NaN absolute", Tolerance{Abs: math.NaN()}, false},
		{"relative of one", Tolerance{Rel: 1}, false},
		{"NaN relative", Tolerance{Rel: math.NaN()}, false},
		{"relative below minimum", Tolerance{Rel: MinRel / 10}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.tol.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tt.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestWithin(t *testing.T) {
	t.Parallel()
	nan, inf := math.NaN(), math.Inf(1)
	tests := []struct {
		name string
		tol  Tolerance
		a, b float64
		want bool
	}{
		{"exact equal", Tolerance{}, 1.5, 1.5, true},
		{"exact differs", Tolerance{}, 1.5, math.Nextafter(1.5, 2), false},
		{"negative zero is zero", Tolerance{}, 0, math.Copysign(0, -1), true},
		{"absolute inside", Tolerance{Abs: 0.1}, 1.0, 1.05, true},
		{"absolute on the bound", Tolerance{Abs: 0.5}, 1.0, 1.5, true},
		{"absolute outside", Tolerance{Abs: 0.1}, 1.0, 1.2, false},
		{"relative inside", Tolerance{Rel: 1e-3}, 1000, 1000.5, true},
		{"relative outside", Tolerance{Rel: 1e-3}, 1000, 1002, false},
		{"either bound suffices", Tolerance{Abs: 1e-9, Rel: 1e-3}, 1000, 1000.5, true},
		{"NaN equals NaN", Tolerance{}, nan, nan, true},
		{"NaN never equals a number", Tolerance{Abs: 1e300}, nan, 1, false},
		{"same infinity", Tolerance{}, inf, inf, true},
		{"opposite infinities", Tolerance{Abs: 1e300}, inf, -inf, false},
		{"infinity is not huge", Tolerance{Rel: 0.5}, inf, math.MaxFloat64, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.tol.Within(tt.a, tt.b); got != tt.want {
				t.Errorf("Within(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
			if got := tt.tol.Within(tt.b, tt.a); got != tt.want {
				t.Errorf("Within is not symmetric for (%v, %v)", tt.a, tt.b)
			}
		})
	}
}

func TestBucketText(t *testing.T) {
	t.Parallel()
	abs := Tolerance{Abs: 1}
	rel := Tolerance{Rel: 0.1}
	tests := []struct {
		name string
		tol  Tolerance
		x    float64
		want string
	}{
		{"absolute positive", abs, 1.2, "~2"},
		{"absolute negative", abs, -1.2, "~-3"},
		{"absolute negative zero", abs, math.Copysign(0, -1), "~0"},
		{"too large for absolute", abs, 1e300, "=1e+300"},
		{"relative zero", rel, 0, "~0~"},
		{"relative negative zero", rel, math.Copysign(0, -1), "~0~"},
		{"relative positive", rel, 1, "~0"},
		{"relative negative", rel, -1, "~-~0"},
		{"NaN", abs, math.NaN(), "nan"},
		{"positive infinity", rel, math.Inf(1), "inf"},
		{"negative infinity", rel, math.Inf(-1), "-inf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := string(tt.tol.Bucket(tt.x).AppendText(nil)); got != tt.want {
				t.Errorf("Bucket(%v) text = %q, want %q", tt.x, got, tt.want)
			}
		})
	}
}

// TestBucketTextIsInjective checks that distinct buckets never share text, which the
// checksum relies on.
func TestBucketTextIsInjective(t *testing.T) {
	t.Parallel()
	seen := map[string]Bucket{}
	for _, tol := range []Tolerance{{Abs: 1}, {Rel: 0.1}} {
		for _, x := range []float64{0, 1, -1, 5, -5, 1e300, -1e300, math.NaN(), math.Inf(1), math.Inf(-1)} {
			b := tol.Bucket(x)
			text := string(b.AppendText(nil))
			if prev, ok := seen[text]; ok && prev != b {
				t.Errorf("buckets %+v and %+v share text %q", prev, b, text)
			}
			seen[text] = b
		}
	}
}

// checkNoFalseNegative fails when a and b are not within tol but share a bucket: the one
// outcome that would let a checksum hide a real difference.
func checkNoFalseNegative(t *testing.T, tol Tolerance, a, b float64) {
	t.Helper()
	if tol.Within(a, b) {
		return
	}
	if ba, bb := tol.Bucket(a), tol.Bucket(b); ba == bb {
		t.Fatalf("%+v: %v and %v are not within tolerance but share bucket %q",
			tol, a, b, ba.AppendText(nil))
	}
}

// TestNoFalseNegativesNearTheBound draws pairs at up to three times the tolerance
// apart, where a bucketing mistake would show, across magnitudes.
func TestNoFalseNegativesNearTheBound(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test data, not security
	for range 200_000 {
		absTol := Tolerance{Abs: math.Pow(10, rng.Float64()*12-10)}
		a := (rng.Float64()*2 - 1) * math.Pow(10, rng.Float64()*12)
		checkNoFalseNegative(t, absTol, a, a+(rng.Float64()*6-3)*absTol.Abs)

		relTol := Tolerance{Rel: math.Pow(10, rng.Float64()*10-11)}
		checkNoFalseNegative(t, relTol, a, a*(1+(rng.Float64()*6-3)*relTol.Rel))

		both := Tolerance{Abs: absTol.Abs, Rel: relTol.Rel}
		checkNoFalseNegative(t, both, a, a+(rng.Float64()*6-3)*absTol.Abs)
	}
}

func FuzzNoFalseNegatives(f *testing.F) {
	f.Add(1.0, 1.2, 0.1, 0.0)
	f.Add(1000.0, 1002.0, 0.0, 1e-3)
	f.Add(-1.0, 1.0, 0.0, 0.5)
	f.Add(0.0, 1e-300, 1e-310, 0.0)
	f.Add(4.5e15, 4.5e15+2, 0.5, 0.0)
	f.Fuzz(func(t *testing.T, a, b, absTol, relTol float64) {
		tol := Tolerance{Abs: math.Abs(absTol), Rel: math.Abs(relTol)}
		if tol.Validate() != nil || tol.IsExact() {
			t.Skip()
		}
		checkNoFalseNegative(t, tol, a, b)
	})
}
