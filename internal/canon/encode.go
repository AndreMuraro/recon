package canon

import (
	"crypto/md5" //nolint:gosec // a well-spread hash every engine has, not a security control (ADR 0001)
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/AndreMuraro/recon/internal/tolerance"
)

// Errors returned by [NewEncoder] and the encoding methods. They are wrapped with the
// column name; test with [errors.Is].
var (
	ErrSchema   = errors.New("invalid column specification")
	ErrMismatch = errors.New("value does not match its column")
)

// Column describes one compared column. Columns are encoded in the order given, which
// must be the same on both sides.
type Column struct {
	Name string
	Type Type
	// Scale is the number of decimal places for TypeDecimal: the larger of the two
	// sides' declared scales, so that values are padded, never rounded.
	Scale int32
	// Tolerance applies to TypeFloat only. The zero value compares exactly.
	Tolerance tolerance.Tolerance
}

// ExactFloatDigits is the number of significant digits in the canonical text of a float
// compared without tolerance.
const ExactFloatDigits = 17

// Separator and escape bytes of the canonical form. NULL is written as the escape
// followed by 'N', a sequence no escaped string can produce.
const (
	sep    = '|'
	escape = '\\'
)

// Encoder writes rows of a fixed schema in canonical form. It is safe for concurrent use.
type Encoder struct {
	cols []Column
}

// NewEncoder validates the schema and returns an encoder for it.
func NewEncoder(cols []Column) (*Encoder, error) {
	if len(cols) == 0 {
		return nil, fmt.Errorf("%w: no columns", ErrSchema)
	}
	seen := make(map[string]bool, len(cols))
	for _, c := range cols {
		if err := c.validate(); err != nil {
			return nil, err
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("%w: duplicate column %q", ErrSchema, c.Name)
		}
		seen[c.Name] = true
	}
	return &Encoder{cols: append([]Column(nil), cols...)}, nil
}

func (c Column) validate() error {
	if c.Name == "" {
		return fmt.Errorf("%w: column without a name", ErrSchema)
	}
	if c.Type <= TypeInvalid || c.Type > TypeString {
		return fmt.Errorf("%w: column %q has unknown type %v", ErrSchema, c.Name, c.Type)
	}
	if c.Type == TypeDecimal && (c.Scale < 0 || c.Scale > MaxScale) {
		return fmt.Errorf("%w: column %q has scale %d outside 0 to %d", ErrSchema, c.Name, c.Scale, MaxScale)
	}
	if c.Type != TypeDecimal && c.Scale != 0 {
		return fmt.Errorf("%w: column %q is %v and cannot have a scale", ErrSchema, c.Name, c.Type)
	}
	if !c.Tolerance.IsExact() {
		if c.Type != TypeFloat {
			return fmt.Errorf("%w: column %q is %v; tolerances apply to floats only", ErrSchema, c.Name, c.Type)
		}
		if err := c.Tolerance.Validate(); err != nil {
			return fmt.Errorf("%w: column %q: %w", ErrSchema, c.Name, err)
		}
	}
	return nil
}

// Columns returns a copy of the encoder's schema.
func (e *Encoder) Columns() []Column { return append([]Column(nil), e.cols...) }

// AppendRow appends the canonical encoding of row to dst and returns the extended slice.
// The row must have one value per column, of the column's type.
func (e *Encoder) AppendRow(dst []byte, row []Value) ([]byte, error) {
	if len(row) != len(e.cols) {
		return dst, fmt.Errorf("%w: row has %d values for %d columns", ErrMismatch, len(row), len(e.cols))
	}
	for i, v := range row {
		if i > 0 {
			dst = append(dst, sep)
		}
		var err error
		if dst, err = e.cols[i].appendValue(dst, v); err != nil {
			return dst, err
		}
	}
	return dst, nil
}

// Hash60 returns the row's 60-bit checksum: the first 15 hex digits of the MD5 of its
// canonical encoding, read as an integer. Engines compute the same number in SQL, and a
// sum of these fits DECIMAL(38, 0) for up to about 2^66 rows.
func (e *Encoder) Hash60(row []Value) (uint64, error) {
	enc, err := e.AppendRow(nil, row)
	if err != nil {
		return 0, err
	}
	sum := md5.Sum(enc) //nolint:gosec // see the import
	return binary.BigEndian.Uint64(sum[:8]) >> 4, nil
}

func (c Column) appendValue(dst []byte, v Value) ([]byte, error) {
	if v.typ != c.Type {
		return dst, fmt.Errorf("%w: column %q is %v, value is %v", ErrMismatch, c.Name, c.Type, v.typ)
	}
	if v.null {
		return append(dst, escape, 'N'), nil
	}
	switch c.Type {
	case TypeInt:
		return strconv.AppendInt(dst, v.i, 10), nil
	case TypeDecimal:
		return c.appendDecimal(dst, v)
	case TypeFloat:
		if !c.Tolerance.IsExact() {
			return c.Tolerance.Bucket(v.f).AppendText(dst), nil
		}
		return appendExactFloat(dst, v.f), nil
	case TypeBool:
		if v.i == 1 {
			return append(dst, 't'), nil
		}
		return append(dst, 'f'), nil
	case TypeDate:
		return time.Unix(v.i*secondsPerDay, 0).UTC().AppendFormat(dst, time.DateOnly), nil
	case TypeTimestamp:
		return strconv.AppendInt(dst, v.i, 10), nil
	case TypeString:
		return appendEscaped(dst, v.s), nil
	case TypeInvalid:
	}
	return dst, fmt.Errorf("%w: column %q has unknown type %v", ErrSchema, c.Name, c.Type)
}

// appendDecimal writes v at the column's scale, padding with zeros. A value with more
// decimal places than the column is a mismatch, not something to round.
func (c Column) appendDecimal(dst []byte, v Value) ([]byte, error) {
	if v.sc > c.Scale {
		return dst, fmt.Errorf("%w: column %q has scale %d, value %s has %d decimal places",
			ErrMismatch, c.Name, c.Scale, v.decimalText(), v.sc)
	}
	digits, neg := strings.CutPrefix(v.s, "-")
	digits += strings.Repeat("0", int(c.Scale-v.sc))
	whole, frac := digits[:len(digits)-int(c.Scale)], digits[len(digits)-int(c.Scale):]
	if whole = strings.TrimLeft(whole, "0"); whole == "" {
		whole = "0"
	}
	if neg {
		dst = append(dst, '-')
	}
	dst = append(dst, whole...)
	if c.Scale > 0 {
		dst = append(dst, '.')
		dst = append(dst, frac...)
	}
	return dst, nil
}

// decimalText renders a decimal at its own scale, for error messages.
func (v Value) decimalText() string {
	col := Column{Name: "-", Type: TypeDecimal, Scale: v.sc}
	b, _ := col.appendDecimal(nil, v)
	return string(b)
}

// appendExactFloat writes f with 17 significant digits in scientific notation, correctly
// rounded: the C format %.16e. Seventeen digits identify every float64, and a printf-style
// format is something engines can produce exactly, where their shortest-digit printing
// differs in layout (DuckDB prints 1e5 as "100000.0"). Negative zero is zero; NaN and the
// infinities have fixed spellings, the same as in tolerance buckets.
func appendExactFloat(dst []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(dst, "nan"...)
	case math.IsInf(f, 1):
		return append(dst, "inf"...)
	case math.IsInf(f, -1):
		return append(dst, "-inf"...)
	case f == 0:
		f = 0 // drop the sign of negative zero
	}
	return strconv.AppendFloat(dst, f, 'e', ExactFloatDigits-1, 64)
}

// appendEscaped writes s with the escape and separator bytes escaped.
func appendEscaped(dst []byte, s string) []byte {
	for i := range len(s) {
		if b := s[i]; b == escape || b == sep {
			dst = append(dst, escape)
		}
		dst = append(dst, s[i])
	}
	return dst
}
