package rb

import (
	"math"
	"testing"
)

// Expected values are the authoritative output of Ruby String#to_i / String#to_f (verified against
// ruby 4.0). These are the coercions the renderer relies on for byte-identical output.

func TestToI(t *testing.T) {
	cases := map[string]int{
		"1_000": 1000, "0x1f": 0, "  -5 ": -5, ".5": 0, "1e3": 1, "inf": 0, "nan": 0,
		"120px": 120, "": 0, "  42": 42, "-5": -5, "1e400": 1, "1e-400": 1, "0.15": 0,
		"44.6": 44, "+7": 7, "3.14abc": 3, ".": 0, "-.5": 0,
	}
	for in, want := range cases {
		if got := ToI(in); got != want {
			t.Errorf("ToI(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestToF(t *testing.T) {
	cases := map[string]float64{
		"1_000": 1000, "0x1f": 0, "  -5 ": -5, ".5": 0.5, "1e3": 1000, "inf": 0, "nan": 0,
		"120px": 120, "": 0, "  42": 42, "-5": -5, "1e-400": 0, "0.15": 0.15,
		"44.6": 44.6, "+7": 7, "3.14abc": 3.14, ".": 0, "-.5": -0.5,
	}
	for in, want := range cases {
		if got := ToF(in); got != want {
			t.Errorf("ToF(%q) = %v, want %v", in, got, want)
		}
	}
	// "1e400" overflows to +Infinity in Ruby; ParseFloat's ErrRange value is preserved.
	if got := ToF("1e400"); !math.IsInf(got, 1) {
		t.Errorf("ToF(%q) = %v, want +Inf", "1e400", got)
	}
}

func TestRounding(t *testing.T) {
	// Ruby Float#round is half away from zero; round(1) of 1.25 -> 1.3 (not strconv's half-even 1.2).
	if got := RoundToInt(44.5); got != 45 {
		t.Errorf("RoundToInt(44.5) = %d, want 45", got)
	}
	if got := RoundToInt(-2.5); got != -3 {
		t.Errorf("RoundToInt(-2.5) = %d, want -3", got)
	}
	if got := Fmt1(1.25); got != "1.3" {
		t.Errorf("Fmt1(1.25) = %q, want %q", got, "1.3")
	}
	if got := Fmt1(2.0); got != "2.0" {
		t.Errorf("Fmt1(2.0) = %q, want %q", got, "2.0")
	}
}
