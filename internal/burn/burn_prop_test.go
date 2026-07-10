package burn

import (
	"testing"

	"pgregory.net/rapid"
)

// A weekly projection reports a burn RATE and a time-to-cap. Over any real history both must be
// non-negative: the envelope makes wk monotonic non-decreasing, and Project only fires on a positive
// climb over a positive span, so a negative burn (or a negative hours-to-cap when wk <= 100) would
// mean the reset-boundary or running-max logic broke.
func TestProjectBurnNonNegative(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 40).Draw(t, "n")
		reset := rapid.Float64Range(1e9, 2e9).Draw(t, "reset")
		cur := rapid.Int64Range(1_000_000_000, 1_500_000_000).Draw(t, "t0")

		entries := make([]Entry, n)
		for i := 0; i < n; i++ {
			cur += rapid.Int64Range(1, 7200).Draw(t, "dt") // strictly increasing timestamps
			entries[i] = Entry{
				"t":        cur,
				"wk":       rapid.Float64Range(0, 100).Draw(t, "wk"),
				"wk_reset": reset,
			}
		}

		p, ok := Project(Envelope(entries, "wk", "wk_reset"))
		if !ok {
			return // flat / too-short runs legitimately yield no projection
		}
		if p.BurnPerH < 0 {
			t.Fatalf("negative burn rate %v", p.BurnPerH)
		}
		if p.HoursToCap < 0 { // wk drawn <= 100, so remaining headroom is non-negative
			t.Fatalf("negative hours-to-cap %v (last wk=%v)", p.HoursToCap, p.Wk)
		}
	})
}
