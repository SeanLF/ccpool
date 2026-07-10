package profile

import (
	"testing"

	"pgregory.net/rapid"
)

// ElapsedFraction is a fraction: for ANY window and profile it must land in [0,1], because pace
// (used% - elapsed%) and every downstream verdict assume it. A value outside [0,1] -- or a NaN
// slipping past the clamp (clamp() lets NaN through) -- would silently poison the pace math.
func TestElapsedFractionInUnitInterval(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		windowStart := rapid.Int64Range(0, 2_000_000_000).Draw(t, "windowStart")
		span := rapid.Int64Range(1, 30*86400).Draw(t, "span")
		reset := windowStart + span
		// now may fall before the window, inside it, or past reset -- all must clamp into [0,1].
		now := rapid.Int64Range(windowStart-span, reset+span).Draw(t, "now")

		cfg := drawConfig(t)
		f := cfg.ElapsedFraction(windowStart, now, reset)
		if !(f >= 0 && f <= 1) { // NaN fails both comparisons -> caught here
			t.Fatalf("ElapsedFraction=%v out of [0,1]: start=%d now=%d reset=%d cfg=%+v", f, windowStart, now, reset, cfg)
		}
	})
}

// drawConfig builds a random resolved profile directly, so the property covers the whole Config
// space rather than only what Load's env parsers can produce. (weight()'s non-custom path still
// reads CCPOOL_PACE_FLOOR; a clean env resolves it to the 0.15 default.) Weight vectors are set as a
// PAIR or not at all, mirroring Load: weight() indexes both when either is non-nil, so a lone vector
// would panic and never occurs in practice.
func drawConfig(t *rapid.T) Config {
	days := map[int]bool{}
	for d := 0; d <= 6; d++ {
		if rapid.Bool().Draw(t, "day") {
			days[d] = true
		}
	}
	cfg := Config{
		Days: days,
		H0:   rapid.IntRange(0, 24).Draw(t, "h0"),
		H1:   rapid.IntRange(0, 24).Draw(t, "h1"),
	}
	if rapid.Bool().Draw(t, "custom") {
		cfg.DayWeights = drawWeights(t, 7)
		cfg.HourWeights = drawWeights(t, 24)
	}
	return cfg
}

func drawWeights(t *rapid.T, n int) []float64 {
	w := make([]float64, n)
	g := rapid.Float64Range(0, 5) // finite, non-negative -> no NaN/Inf from the generator itself
	for i := range w {
		w[i] = g.Draw(t, "weight")
	}
	return w
}
