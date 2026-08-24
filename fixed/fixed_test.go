package fixed

import (
	"errors"
	"testing"
)

func TestParseBoundaryAndRoundTrip(t *testing.T) {
	cases := []struct {
		in    string
		scale int
		raw   int64
	}{
		{"32.00", 2, 3200},
		{"0.00", 2, 0},
		{"-5.25", 2, -525},
		{"+5.25", 2, 525},
		{"5", 2, 500},
		{"5.2", 1, 52},
		{"0", 3, 0},
	}
	for _, c := range cases {
		got, err := Parse(c.in, c.scale)
		if err != nil {
			t.Fatalf("Parse(%q,%d): unexpected error %v", c.in, c.scale, err)
		}
		if got.Raw != c.raw || got.Scale != c.scale {
			t.Fatalf("Parse(%q,%d) = %+v, want raw=%d scale=%d", c.in, c.scale, got, c.raw, c.scale)
		}
		if got.String() != normalize(c.in, c.scale) {
			t.Fatalf("String() = %q, want %q", got.String(), normalize(c.in, c.scale))
		}
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	cases := []struct {
		in    string
		scale int
		want  error
	}{
		{"abc", 2, ErrInvalidFormat},
		{"1.2.3", 2, ErrInvalidFormat},
		{"1..2", 2, ErrInvalidFormat},
		{"--1", 2, ErrInvalidFormat},
		{"1e3", 2, ErrInvalidFormat},
		{"", 2, ErrInvalidFormat},
		{"1.234", 2, ErrTooManyDecimalPlaces},
		{"9223372036854775808", 0, ErrOverflow},
		{"99999999999999999999.99", 2, ErrOverflow},
		{"1.2", -1, ErrNegativeScale},
	}
	for _, c := range cases {
		_, err := Parse(c.in, c.scale)
		if !errors.Is(err, c.want) {
			t.Fatalf("Parse(%q,%d) = %v, want %v", c.in, c.scale, err, c.want)
		}
	}
}

func TestCompareAcrossScales(t *testing.T) {
	a := MustParse("1.5", 1)
	b := MustParse("1.50", 2)
	if a.Compare(b) != 0 {
		t.Fatalf("Compare across scales: got %d, want 0", a.Compare(b))
	}
	if MustParse("0.80", 2).Compare(MustParse("0.79", 2)) != 1 {
		t.Fatal("conductivity threshold compare failed")
	}
}

func TestDivByZero(t *testing.T) {
	a := MustParse("10.0", 1)
	z := MustParse("0.0", 1)
	if _, err := a.Div(z); !errors.Is(err, ErrDivisionByZero) {
		t.Fatalf("Div by zero = %v, want ErrDivisionByZero", err)
	}
}

func TestAddOverflow(t *testing.T) {
	a := MustParse("9223372036854775807", 0)
	if _, err := a.Add(a); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Add overflow = %v, want ErrOverflow", err)
	}
}

// normalize rewrites an input to the canonical String() form for comparison.
func normalize(in string, scale int) string {
	d := MustParse(in, scale)
	return d.String()
}
