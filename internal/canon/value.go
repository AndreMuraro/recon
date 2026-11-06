// Package canon defines the canonical encoding of rows: the one byte sequence each
// engine's SQL must reproduce for a row, so that checksums computed in different
// warehouses can be compared. This package is the reference; each dialect's encoder is
// tested against it.
package canon

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Type is a column's logical type, independent of any engine's type names.
type Type uint8

// The logical types recon compares.
const (
	TypeInvalid Type = iota
	TypeInt
	TypeDecimal
	TypeFloat
	TypeBool
	TypeDate
	TypeTimestamp
	TypeString
)

// String returns the type's name, as used in error messages.
func (t Type) String() string {
	switch t {
	case TypeInt:
		return "int"
	case TypeDecimal:
		return "decimal"
	case TypeFloat:
		return "float"
	case TypeBool:
		return "bool"
	case TypeDate:
		return "date"
	case TypeTimestamp:
		return "timestamp"
	case TypeString:
		return "string"
	case TypeInvalid:
		return "invalid"
	default:
		return fmt.Sprintf("Type(%d)", uint8(t))
	}
}

// Errors returned when building values. They are wrapped with details; test with
// [errors.Is].
var (
	ErrSyntax = errors.New("invalid syntax")
	ErrRange  = errors.New("value out of range")
)

// Value is one cell. The zero Value is invalid; build values with the constructors.
// Values are small and are passed and stored by value.
type Value struct {
	typ  Type
	null bool
	i    int64   // TypeInt, TypeBool (0 or 1), TypeDate (days since 1970-01-01), TypeTimestamp (µs since epoch, UTC)
	f    float64 // TypeFloat
	s    string  // TypeString, TypeDecimal (normalised digits, see Decimal)
	sc   int32   // TypeDecimal scale
}

// Type returns the value's type.
func (v Value) Type() Type { return v.typ }

// IsNull reports whether the value is SQL NULL.
func (v Value) IsNull() bool { return v.null }

// Null returns a NULL of type t.
func Null(t Type) Value { return Value{typ: t, null: true} }

// Int returns an integer value.
func Int(n int64) Value { return Value{typ: TypeInt, i: n} }

// Float returns a floating-point value.
func Float(f float64) Value { return Value{typ: TypeFloat, f: f} }

// Bool returns a boolean value.
func Bool(b bool) Value {
	v := Value{typ: TypeBool}
	if b {
		v.i = 1
	}
	return v
}

// String returns a string value. The bytes are kept as given: no Unicode normalisation,
// trimming or case folding.
func String(s string) Value { return Value{typ: TypeString, s: s} }

// Engines agree on dates and timestamps only in years 1 to 9999.
var (
	minTime = time.Date(1, time.January, 1, 0, 0, 0, 0, time.UTC)
	maxTime = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// Date returns the calendar date of t in t's location.
func Date(t time.Time) (Value, error) {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if day.Before(minTime) || !day.Before(maxTime) {
		return Value{}, fmt.Errorf("%w: date %s is outside years 1 to 9999", ErrRange, day.Format(time.DateOnly))
	}
	return Value{typ: TypeDate, i: day.Unix() / secondsPerDay}, nil
}

const secondsPerDay = 24 * 60 * 60

// Timestamp returns the instant t at microsecond precision. Sub-microsecond parts are
// an error rather than silently dropped: engines round them differently.
func Timestamp(t time.Time) (Value, error) {
	if t.Before(minTime) || !t.Before(maxTime) {
		return Value{}, fmt.Errorf("%w: timestamp %s is outside years 1 to 9999", ErrRange, t.Format(time.RFC3339Nano))
	}
	if t.Nanosecond()%int(time.Microsecond) != 0 {
		return Value{}, fmt.Errorf("%w: timestamp %s has sub-microsecond precision", ErrRange, t.Format(time.RFC3339Nano))
	}
	return Value{typ: TypeTimestamp, i: t.UnixMicro()}, nil
}

// Decimal parses an exact decimal such as "-12.50". Exponents, a leading '+', spaces and
// thousands separators are rejected. The scale is the number of digits after the point.
func Decimal(s string) (Value, error) {
	digits, scale, neg, err := parseDecimal(s)
	if err != nil {
		return Value{}, err
	}
	if neg && strings.Trim(digits, "0") == "" {
		neg = false // -0.00 is 0.00
	}
	if neg {
		digits = "-" + digits
	}
	return Value{typ: TypeDecimal, s: digits, sc: scale}, nil
}

// MaxScale is the largest decimal scale engines agree on (Snowflake and Databricks allow
// up to 37 and 38 digits of precision).
const MaxScale = 37

func parseDecimal(s string) (digits string, scale int32, neg bool, err error) {
	syntax := func() (string, int32, bool, error) {
		return "", 0, false, fmt.Errorf("%w: decimal %q", ErrSyntax, s)
	}
	rest := s
	if strings.HasPrefix(rest, "-") {
		neg, rest = true, rest[1:]
	}
	whole, frac, hasPoint := strings.Cut(rest, ".")
	if whole == "" || (hasPoint && frac == "") || !allDigits(whole) || !allDigits(frac) {
		return syntax()
	}
	if len(frac) > MaxScale {
		return "", 0, false, fmt.Errorf("%w: decimal %q has more than %d digits after the point", ErrRange, s, MaxScale)
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	return whole + frac, int32(len(frac)), neg, nil //nolint:gosec // len(frac) <= MaxScale
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
