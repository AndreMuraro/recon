package tolerance

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// ErrExactText reports text that is not an exact float in canonical form.
var ErrExactText = errors.New("not an exact float text")

// AppendExact writes the canonical text of a float compared exactly (ADR 0009): its
// binary significand and exponent, m·2^q, as "<m>p<q>", with a minus sign for negative
// values. For normal floats m is the full 53-bit significand, in [2^52, 2^53), and
// q = e - 52 where 2^e <= |x| < 2^(e+1); for subnormals q = -1074. Every float64 has
// exactly one such text, and an engine can compute it with ln, power and division by
// powers of two, which are exact, unlike decimal formatting, whose rounding differs
// between C and Java.
//
// Zero, of either sign, is "0"; NaN and the infinities are "nan", "inf" and "-inf", the
// same as in buckets.
func AppendExact(dst []byte, x float64) []byte {
	switch {
	case math.IsNaN(x):
		return append(dst, "nan"...)
	case math.IsInf(x, 1):
		return append(dst, "inf"...)
	case math.IsInf(x, -1):
		return append(dst, "-inf"...)
	case x == 0:
		return append(dst, '0')
	}
	bits := math.Float64bits(x)
	if bits>>63 == 1 {
		dst = append(dst, '-')
	}
	exp := int64(bits>>52) & 0x7ff
	m := bits & (1<<52 - 1)
	q := int64(-1074)
	if exp != 0 {
		m |= 1 << 52
		q = exp - 1075
	}
	dst = strconv.AppendUint(dst, m, 10)
	dst = append(dst, 'p')
	return strconv.AppendInt(dst, q, 10)
}

// ParseExact reads the text AppendExact writes. It accepts only canonical texts, so that
// a dialect producing anything else is caught rather than silently read.
func ParseExact(s string) (float64, error) {
	switch s {
	case "nan":
		return math.NaN(), nil
	case "inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	case "0":
		return 0, nil
	}
	ms, qs, ok := strings.Cut(strings.TrimPrefix(s, "-"), "p")
	m, errM := strconv.ParseUint(ms, 10, 64)
	q, errQ := strconv.ParseInt(qs, 10, 32)
	if !ok || errM != nil || errQ != nil {
		return 0, errors.Join(ErrExactText, errors.New(strconv.Quote(s)))
	}
	x := math.Ldexp(float64(m), int(q))
	if s[0] == '-' {
		x = -x
	}
	if string(AppendExact(nil, x)) != s {
		return 0, errors.Join(ErrExactText, errors.New(strconv.Quote(s)+" is not in canonical form"))
	}
	return x, nil
}
