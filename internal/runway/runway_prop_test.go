package runway

import (
	"testing"

	"github.com/SeanLF/ccpool/internal/burn"
	"pgregory.net/rapid"
)

// Runway must be monotonic in burn: burning a bigger share of the pool over the same run (a higher
// Dpct, i.e. a higher rate) can only SHORTEN the working-hours the budget affords, never lengthen
// them. A regression that inverted this would tell you to keep going when you should ease off.
func TestRunwayMonotonicInBurn(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := rapid.Int64Range(1_000_000_000, 2_000_000_000).Draw(t, "now")
		firstT := now - rapid.Int64Range(3600, 20*86400).Draw(t, "runAge")
		lastT := firstT + rapid.Int64Range(3600, 10*86400).Draw(t, "runSpan")
		reset := now + rapid.Int64Range(3600, 8*86400).Draw(t, "toReset")
		used := rapid.Float64Range(0, 99).Draw(t, "used")

		d1 := rapid.Float64Range(1, 50).Draw(t, "dpct1")
		d2 := d1 + rapid.Float64Range(0.1, 50).Draw(t, "extra") // strictly higher burn

		mk := func(d float64) burn.Projection {
			return burn.Projection{Dpct: d, FirstT: firstT, LastT: lastT}
		}
		r1, ok1 := Estimate(used, reset, mk(d1), true, now)
		r2, ok2 := Estimate(used, reset, mk(d2), true, now)
		if !ok1 || !ok2 {
			t.Fatalf("Estimate returned !ok on a well-formed run (d1=%v d2=%v)", d1, d2)
		}
		const eps = 1e-9
		if r2.BudgetH > r1.BudgetH+eps {
			t.Fatalf("higher burn gave MORE budget-hours: d1=%v -> %v, d2=%v -> %v", d1, r1.BudgetH, d2, r2.BudgetH)
		}
		if r2.Hours > r1.Hours+eps {
			t.Fatalf("higher burn gave MORE runway: d1=%v -> %vh, d2=%v -> %vh", d1, r1.Hours, d2, r2.Hours)
		}
	})
}
