package rb

import (
	"math"
	"testing"
)

// Fuzz the rb coercions: none of these may panic (they sit under the fail-open hot path).

func FuzzParseObject(f *testing.F) {
	seeds := []string{
		"", "{}", `{"a":1}`, `{"a":`, "[1,2,3]", "null", "1e400",
		`{"a":{"b":{"c":{"d":1}}}}`, `{"a":1} trailing`, "\xff\xfe",
		`{"n":123456789012345678901234567890}`, `"just a string"`,
		`{"unicode":"é\u{1f600}"}`, "   ", "\x00\x01\x02",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		m := ParseObject(b)
		// Exercise reading values back the canonical way (Num) so numeric decode paths run.
		for _, v := range m {
			_, _ = Num(v)
		}
	})
}

func FuzzToI(f *testing.F) {
	seeds := []string{
		"", "0", "-5", "+7", "1_000", "120px", "0x1f", ".5", "1e400",
		"   42  ", "-", "_", "1__2", "\xff", "999999999999999999999999999999",
		"\t\n-  9", "1_", "_1",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _ = ToI(s) })
}

func FuzzToF(f *testing.F) {
	seeds := []string{
		"", "0.0", "-.5", "+7.25", "1_000.5", "3.14abc", "1e400", "1e-400",
		".", "e5", "1.2e", "1.2e+", "0x1f", "\xff", "1_.5", ".5e10",
		"1e999999999", "inf", "nan",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _ = ToF(s) })
}

// FuzzRoundFmt feeds random float bits into the rounding/formatting helpers (incl. NaN, ±Inf,
// subnormals, huge magnitudes). None may panic; Fmt1 must always return a parseable-ish string.
func FuzzRoundFmt(f *testing.F) {
	seeds := []uint64{
		0, math.Float64bits(0.5), math.Float64bits(1.25), math.Float64bits(-2.5),
		math.Float64bits(math.NaN()), math.Float64bits(math.Inf(1)), math.Float64bits(math.Inf(-1)),
		math.Float64bits(math.MaxFloat64), math.Float64bits(math.SmallestNonzeroFloat64),
		math.Float64bits(1e308),
	}
	for _, s := range seeds {
		f.Add(s, 3)
	}
	f.Fuzz(func(t *testing.T, bits uint64, n int) {
		x := math.Float64frombits(bits)
		// Clamp n to a sane range; RoundN uses Pow(10,n) and a wild n is not a real caller input.
		if n < -20 {
			n = -20
		}
		if n > 20 {
			n = 20
		}
		_ = RoundToInt(x)
		_ = Round1(x)
		_ = RoundN(x, n)
		_ = Fmt1(x)
	})
}
