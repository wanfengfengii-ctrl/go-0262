// Package fixed provides the integer-scaled fixed-point decimal used across
// the NectarGate domain for physical readings (DNA Ct, HMF, amylase activity,
// moisture, conductivity, acidity). The represented value is Raw * 10^-Scale.
//
// Domain rule: all DNA Ct values and chemistry readings are expressed as
// fixed-decimal integers. Parsing must check length, sign, decimal places,
// division by zero and overflow; failed arithmetic must not produce derived
// evidence.
package fixed

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

// Decimal is an integer-scaled fixed-point decimal value.
type Decimal struct {
	Raw   int64
	Scale int
}

// Sentinel errors returned by parsing and arithmetic.
var (
	ErrInvalidFormat        = errors.New("fixed: invalid decimal format")
	ErrTooManyDecimalPlaces = errors.New("fixed: too many decimal places")
	ErrNegativeScale        = errors.New("fixed: negative scale")
	ErrOverflow             = errors.New("fixed: overflow")
	ErrDivisionByZero       = errors.New("fixed: division by zero")
	ErrScaleMismatch        = errors.New("fixed: scale mismatch")
)

// Zero returns the zero value at the given scale.
func Zero(scale int) Decimal { return Decimal{Scale: scale} }

// Parse parses a decimal string into a fixed-point value with exactly the
// given scale. It enforces the domain length, sign, decimal-place, digit and
// int64-overflow checks.
func Parse(s string, scale int) (Decimal, error) {
	if scale < 0 {
		return Decimal{}, ErrNegativeScale
	}
	if len(s) == 0 || len(s) > 64 {
		return Decimal{}, ErrInvalidFormat
	}

	neg := false
	body := s
	switch s[0] {
	case '+':
		body = s[1:]
	case '-':
		neg = true
		body = s[1:]
	}
	if len(body) == 0 {
		return Decimal{}, ErrInvalidFormat
	}

	intPart := body
	fracPart := ""
	if i := strings.IndexByte(body, '.'); i >= 0 {
		intPart, fracPart = body[:i], body[i+1:]
		if strings.IndexByte(fracPart, '.') >= 0 {
			return Decimal{}, ErrInvalidFormat
		}
	}
	if intPart == "" {
		intPart = "0"
	}
	if !isDigits(intPart) || !isDigits(fracPart) {
		return Decimal{}, ErrInvalidFormat
	}
	if len(fracPart) > scale {
		return Decimal{}, ErrTooManyDecimalPlaces
	}
	for len(fracPart) < scale {
		fracPart += "0"
	}

	sign := ""
	if neg {
		sign = "-"
	}
	raw, err := strconv.ParseInt(sign+intPart+fracPart, 10, 64)
	if err != nil {
		return Decimal{}, ErrOverflow
	}
	return Decimal{Raw: raw, Scale: scale}, nil
}

// MustParse is Parse that panics on error; intended for trusted constants.
func MustParse(s string, scale int) Decimal {
	d, err := Parse(s, scale)
	if err != nil {
		panic(err)
	}
	return d
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// String renders the decimal with exactly Scale fractional digits.
func (d Decimal) String() string {
	if d.Scale == 0 {
		return strconv.FormatInt(d.Raw, 10)
	}
	neg := d.Raw < 0
	raw := d.Raw
	if neg {
		raw = -raw
	}
	s := strconv.FormatInt(raw, 10)
	for len(s) <= d.Scale {
		s = "0" + s
	}
	intPart := s[:len(s)-d.Scale]
	fracPart := s[len(s)-d.Scale:]
	out := intPart + "." + fracPart
	if neg {
		out = "-" + out
	}
	return out
}

// Compare returns -1, 0 or 1 depending on whether d is less than, equal to or
// greater than o. Values with differing scales are compared by value, not by
// raw representation.
func (d Decimal) Compare(o Decimal) int {
	if d.Scale == o.Scale {
		return cmp(d.Raw, o.Raw)
	}
	l := new(big.Int).Mul(big.NewInt(d.Raw), pow10(o.Scale))
	r := new(big.Int).Mul(big.NewInt(o.Raw), pow10(d.Scale))
	return l.Cmp(r)
}

func cmp(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Add returns d+o. Both values must share the same scale.
func (d Decimal) Add(o Decimal) (Decimal, error) {
	if d.Scale != o.Scale {
		return Decimal{}, ErrScaleMismatch
	}
	return addRaw(d.Raw, o.Raw, d.Scale)
}

// Sub returns d-o. Both values must share the same scale.
func (d Decimal) Sub(o Decimal) (Decimal, error) {
	if d.Scale != o.Scale {
		return Decimal{}, ErrScaleMismatch
	}
	return subRaw(d.Raw, o.Raw, d.Scale)
}

// Div returns d/o with the result rounded to d.Scale decimal places.
// It reports ErrDivisionByZero when o is zero and ErrOverflow on overflow.
func (d Decimal) Div(o Decimal) (Decimal, error) {
	if o.Raw == 0 {
		return Decimal{}, ErrDivisionByZero
	}
	scale := d.Scale
	if o.Scale > scale {
		scale = o.Scale
	}
	// numerator = d.Raw * 10^scale (aligned to result scale)
	num := new(big.Int).Mul(big.NewInt(d.Raw), pow10(scale-d.Scale))
	den := new(big.Int).Mul(big.NewInt(o.Raw), pow10(scale-o.Scale))
	q := new(big.Int).Quo(num, den)
	if !q.IsInt64() {
		return Decimal{}, ErrOverflow
	}
	return Decimal{Raw: q.Int64(), Scale: scale}, nil
}

func addRaw(a, b int64, scale int) (Decimal, error) {
	s := new(big.Int).Add(big.NewInt(a), big.NewInt(b))
	if !s.IsInt64() {
		return Decimal{}, ErrOverflow
	}
	return Decimal{Raw: s.Int64(), Scale: scale}, nil
}

func subRaw(a, b int64, scale int) (Decimal, error) {
	s := new(big.Int).Sub(big.NewInt(a), big.NewInt(b))
	if !s.IsInt64() {
		return Decimal{}, ErrOverflow
	}
	return Decimal{Raw: s.Int64(), Scale: scale}, nil
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
