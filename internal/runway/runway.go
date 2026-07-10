// Package runway answers "how many WORKING hours of pool do I have left before the weekly reset?" —
// the actionable reframe of "% left" (same shape as SRE error-budget burn-rate alerting). Two
// refinements over the raw rate: burn is re-measured per ACTIVE hour (integral of the Profile weight
// over the run, so idle doesn't dilute it), and the answer is min(budget-affords, calendar-has-left)
// — which one BINDS is the verdict. Weekly on purpose: the weekly pool is the only budget that
// doesn't recover while you sleep. Burn is bursty, so the phrasing reports a RANGE, never a point.
package runway

import (
	"math"
	"strconv"

	"github.com/SeanLF/ccpool/internal/burn"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/profile"
	"github.com/SeanLF/ccpool/internal/rb"
)

// Bind names which side of the min() binds the runway.
type Bind int

const (
	Budget Bind = iota
	Calendar
)

// Result is the estimated runway: the working-hours the budget affords vs the calendar has left, and
// which one binds.
type Result struct {
	Hours, Low, High, BudgetH, CalH float64
	Bind                            Bind
}

// Asymmetric fat-tail band + a per-active-hour density floor. Read fresh (env) so per-fixture test
// env is honoured; defaults match the Ruby module constants.
func fast() float64       { return env.Float("CCPOOL_RUNWAY_FAST", 1.5) }        // rate could be this much higher
func slow() float64       { return env.Float("CCPOOL_RUNWAY_SLOW", 0.7) }        // ...or this much lower
func minDensity() float64 { return env.Float("CCPOOL_RUNWAY_MIN_DENSITY", 0.5) } // floor active-hours at this fraction of wall

// activeHours over [a, b] (the Profile activity integral in hours), but never fewer than minDensity
// of the wall span.
func activeHours(cfg profile.Config, a, b int64) float64 {
	return math.Max(cfg.Integral(a, b)/3600.0, float64(b-a)/3600.0*minDensity())
}

// Estimate re-measures the projected run in working hours and bounds the runway. ok=false when there
// is no usable run (proj absent, non-positive climb, or a degenerate span).
func Estimate(used float64, reset int64, proj burn.Projection, hasProj bool, now int64) (Result, bool) {
	if !hasProj || proj.Dpct <= 0 || proj.FirstT == 0 || proj.LastT == 0 {
		return Result{}, false
	}
	cfg := profile.Load()
	activeH := activeHours(cfg, proj.FirstT, proj.LastT)
	calH := cfg.Integral(now, reset) / 3600.0
	if activeH <= 0 || calH <= 0 {
		return Result{}, false
	}
	rate := proj.Dpct / activeH // % of pool per WORKING hour
	remaining := math.Max(100.0-used, 0.0)
	budgetH := remaining / rate
	res := Result{
		Hours:   math.Min(budgetH, calH),
		Low:     math.Min(remaining/(rate*fast()), calH),
		High:    math.Min(remaining/(rate*slow()), calH),
		BudgetH: budgetH,
		CalH:    calH,
		Bind:    Calendar,
	}
	if budgetH < calH {
		res.Bind = Budget
	}
	return res, true
}

// Phrase is the one-line human phrasing. Budget-limited surfaces the working-hours range; calendar-
// limited just says the week wins, so burn freely.
func Phrase(r Result, toReset int64) string {
	if r.Bind == Budget {
		lo := rb.RoundToInt(r.Low)
		hi := rb.RoundToInt(r.High)
		span := "~" + strconv.Itoa(lo)
		if lo != hi {
			span += "-" + strconv.Itoa(hi)
		}
		return span + " working-hours of pool left -> at your active-hour burn you'd throttle before reset (" + dur(toReset) + " out)"
	}
	return "budget outlasts the week -> reset (" + dur(toReset) + ") comes first with headroom, burn freely"
}

// dur is Runway's own coarse phrasing: "3d 5h" / "5h" (no minutes, unlike CCPool.dur).
func dur(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	if secs > 1<<62 {
		secs = 1 << 62
	}
	h := secs / 3600
	d := h / 24
	h %= 24
	if d > 0 {
		return strconv.FormatInt(d, 10) + "d " + strconv.FormatInt(h, 10) + "h"
	}
	return strconv.FormatInt(h, 10) + "h"
}
