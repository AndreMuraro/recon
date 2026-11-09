// Package tolerance decides when two floating-point values count as equal, and maps
// values to buckets that checksums can compare exactly.
//
// The guarantee that matters is one-sided: two values that are not [Tolerance.Within]
// each other always land in different buckets, so a checksum over buckets can never
// hide a difference beyond tolerance. The reverse does not hold: values close to a bucket
// edge can be within tolerance and still land in different buckets. That costs a
// row-level comparison, never a wrong answer. See docs/adr/0001-comparison-model.md.
package tolerance

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// MinRel is the smallest relative tolerance accepted. Below it, the rounding error of
// math.Log is comparable to the bucket width and the guarantee above cannot be kept.
const MinRel = 1e-12

// MaxIndex bounds |x/w| for absolute buckets. Up to 2^52 the quotient is exact to within
// half a unit, so two values more than two bucket widths apart cannot round into one
// bucket. Larger values are compared exactly instead; see [Tolerance.AbsLimit].
const MaxIndex = 1 << 52

// ErrInvalid reports a tolerance that cannot be used.
var ErrInvalid = errors.New("invalid tolerance")

// Tolerance is the allowed difference between two values: absolute, relative, or both.
// Two values are equal if either bound holds, as in Python's math.isclose. The zero
// value means exact equality.
type Tolerance struct {
	// Abs is the largest allowed |a-b|.
	Abs float64
	// Rel is the largest allowed |a-b| / max(|a|, |b|). It must be below 1.
	Rel float64
}

// Validate reports whether t can be used for comparisons and buckets.
func (t Tolerance) Validate() error {
	switch {
	case math.IsNaN(t.Abs) || math.IsInf(t.Abs, 0) || t.Abs < 0:
		return fmt.Errorf("%w: absolute tolerance %v must be finite and not negative", ErrInvalid, t.Abs)
	case math.IsNaN(t.Rel) || t.Rel < 0 || t.Rel >= 1:
		return fmt.Errorf("%w: relative tolerance %v must be in [0, 1)", ErrInvalid, t.Rel)
	case t.Rel > 0 && t.Rel < MinRel:
		return fmt.Errorf("%w: relative tolerance %v is below the supported minimum %v", ErrInvalid, t.Rel, MinRel)
	}
	return nil
}

// IsExact reports whether t requires exact equality.
func (t Tolerance) IsExact() bool { return t.Abs == 0 && t.Rel == 0 }

// Within reports whether a and b are equal under t.
//
// NaN equals NaN, and an infinity equals only the same infinity. These are the rules a
// parity check needs, not IEEE 754's.
func (t Tolerance) Within(a, b float64) bool {
	switch {
	case math.IsNaN(a) || math.IsNaN(b):
		return math.IsNaN(a) && math.IsNaN(b)
	case math.IsInf(a, 0) || math.IsInf(b, 0):
		return a == b
	case a == b:
		return true
	}
	diff := math.Abs(a - b)
	return diff <= t.Abs || diff <= t.Rel*math.Max(math.Abs(a), math.Abs(b))
}

// Bucket is the class a value falls into under a tolerance. Equal buckets are a
// necessary condition for equality, never a sufficient one.
type Bucket struct {
	kind  bucketKind
	index int64
	exact float64 // the value itself, when it cannot be bucketed
}

type bucketKind uint8

const (
	bucketIndex    bucketKind = iota // absolute bucket, or the log bucket of a positive value
	bucketNegIndex                   // log bucket of a negative value
	bucketZero                       // zero under a relative tolerance
	bucketNaN
	bucketPosInf
	bucketNegInf
	bucketExact // too large to bucket; compared exactly
)

// Bucket returns the bucket of x under t. t must be valid and not exact.
//
// With an absolute tolerance, buckets are Abs/2 wide. When both bounds are set, only the
// absolute one is used: a pair that fails Within is beyond Abs in particular, so
// absolute buckets alone keep the guarantee. With only a relative tolerance, buckets are
// ln(1 + Rel/2) wide on the log scale, with zero and each sign kept apart.
func (t Tolerance) Bucket(x float64) Bucket {
	switch {
	case math.IsNaN(x):
		return Bucket{kind: bucketNaN}
	case math.IsInf(x, 1):
		return Bucket{kind: bucketPosInf}
	case math.IsInf(x, -1):
		return Bucket{kind: bucketNegInf}
	}
	if t.UsesAbsBuckets() {
		// Comparing x with the limit, rather than x/w with MaxIndex, never divides a huge
		// value: engines such as Postgres raise an error on float overflow.
		if math.Abs(x) >= t.AbsLimit() {
			return Bucket{kind: bucketExact, exact: x}
		}
		return Bucket{kind: bucketIndex, index: int64(math.Floor(x / t.AbsWidth()))}
	}
	if x == 0 {
		return Bucket{kind: bucketZero}
	}
	kind := bucketIndex
	if x < 0 {
		kind = bucketNegIndex
	}
	return Bucket{kind: kind, index: int64(math.Floor(LogAbs(x) / t.LogWidth()))}
}

// smallestNormal is the smallest positive normal float64, 2^-1022.
const smallestNormal = 0x1p-1022

// LogAbs returns ln|x| for x != 0, as relative buckets and SQL dialects compute it.
//
// Subnormal inputs are scaled into the normal range first. Go's math.Log is wrong for
// them on amd64, whose assembly implementation returns about -709.09 for 5e-324 instead
// of -744.44 (https://go.dev/issue/56600, open since 2022). The C library, and so every
// engine, gets them right. Found by the DuckDB conformance test.
func LogAbs(x float64) float64 {
	a := math.Abs(x)
	if a < smallestNormal {
		return math.Log(a*0x1p52) - 52*math.Ln2
	}
	return math.Log(a)
}

// UsesAbsBuckets reports whether t buckets on the absolute scale (Abs > 0) rather than the
// log scale.
func (t Tolerance) UsesAbsBuckets() bool { return t.Abs > 0 }

// AbsWidth is the width of absolute buckets, Abs/2. SQL dialects use this exact value.
func (t Tolerance) AbsWidth() float64 { return t.Abs / 2 }

// AbsLimit is the magnitude from which values are compared exactly rather than bucketed:
// MaxIndex absolute buckets. Multiplying by a power of two is exact, so this is the
// precise point where |x / AbsWidth()| reaches MaxIndex. SQL dialects use this value.
func (t Tolerance) AbsLimit() float64 { return MaxIndex * t.AbsWidth() }

// LogWidth is the width of relative buckets on the natural-log scale, ln(1 + Rel/2). SQL
// dialects use this exact value.
func (t Tolerance) LogWidth() float64 { return math.Log1p(t.Rel / 2) }

// AppendText appends the bucket's canonical text to dst. Distinct buckets always have
// distinct text, and no bucket's text contains '|' or '\'.
func (b Bucket) AppendText(dst []byte) []byte {
	switch b.kind {
	case bucketIndex:
		dst = append(dst, '~')
		return strconv.AppendInt(dst, b.index, 10)
	case bucketNegIndex:
		dst = append(dst, "~-~"...)
		return strconv.AppendInt(dst, b.index, 10)
	case bucketZero:
		return append(dst, "~0~"...)
	case bucketNaN:
		return append(dst, "nan"...)
	case bucketPosInf:
		return append(dst, "inf"...)
	case bucketNegInf:
		return append(dst, "-inf"...)
	case bucketExact:
		return AppendExact(append(dst, '='), b.exact)
	default:
		panic("tolerance: unknown bucket kind")
	}
}
