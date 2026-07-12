// Package report holds the shared readout building blocks for the on-demand commands (status,
// check, run): the dollar/duration/time formatters, and resolve_weekly — the fresh/estimated/stale
// 3-tier read that fuses the reconciled pool % with a ccusage-calibrated extrapolation of a stale %.
package report

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/calib"
	"github.com/SeanLF/ccpool/internal/clock"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/profile"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

// Confidence tiers the freshness of the resolved weekly window.
type Confidence int

const (
	Fresh Confidence = iota
	Estimated
	Stale
)

// Weekly is the resolved weekly pool: used %, reset epoch, snapshot age, and the confidence tier.
type Weekly struct {
	Used       float64
	Reset      int64
	Age        int64
	Confidence Confidence
}

// margin / coast are the shared pace knobs (also read by warn/check/downshift so they never disagree).
func margin() float64 {
	return env.Float("CCPOOL_PACE_MARGIN", 3)
}

func coast() int64 {
	return env.Int64("CCPOOL_COAST_SECS", 43200)
}

// ResolveWeekly is the 3-tier read: fresh official snapshot > stale-but-extrapolated-from-accrued-
// cost > stale-shown-with-warning. ok=false when no plausible weekly window exists at all. The store
// is threaded in from the command so snapshots and the calibration/blocks caches share one open.
func ResolveWeekly(s *store.Store, now int64) (Weekly, bool) {
	snaps := pool.LoadSnapshots(s)
	w, ok := pool.GetWindow(snaps, "seven_day", now, pool.Week+86400)
	if !ok {
		return Weekly{}, false
	}
	age, _ := pool.DataAge(snaps, now) // 0 if none, matching Ruby `age = wk[:age] || 0`
	res := Weekly{Used: w.Used, Reset: w.Reset, Age: age, Confidence: Fresh}
	if age <= pool.Stale() {
		return res, true
	}
	// Stale: try to extrapolate the % forward from accrued ccusage cost since the snapshot.
	if dpp, ok := calib.DollarPerPct(s, now, false); ok {
		if accrued, ok := calib.CostSince(s, now-age, now); ok && accrued >= 0 {
			res.Used = clamp(w.Used+accrued/dpp, 0, 100)
			res.Confidence = Estimated
			return res, true
		}
	}
	res.Confidence = Stale
	return res, true
}

// Stamp is the freshness suffix appended to the weekly line.
func (w Weekly) Stamp() string {
	switch w.Confidence {
	case Estimated:
		return "  ·  ~estimated (snapshot " + fmtx.Dur(w.Age) + " old + accrued cost)"
	case Stale:
		return "  ·  ⚠ stale: snapshot " + fmtx.Dur(w.Age) + " old, may be behind (open Claude Code)"
	default:
		if w.Age > 300 {
			return fmt.Sprintf("  ·  data %dm old", w.Age/60)
		}
		return ""
	}
}

// PacePhrase renders the pace verdict shared by status (and echoed by downshift).
func PacePhrase(p pool.Pace) string {
	if p.ToReset < coast() {
		return "reset in " + fmtx.Dur(p.ToReset) + " -- unspent budget is use-it-or-lose-it, burn freely"
	}
	frame := "of the week elapsed"
	if profile.Load().Scheduled() {
		frame = "of your work-rhythm pace"
	}
	el := rb.RoundToInt(p.ElapsedPct)
	m := margin()
	switch {
	case p.Delta > m:
		return fmt.Sprintf("%d pts AHEAD of pace (%d%% %s) -- burning fast", rb.RoundToInt(p.Delta), el, frame)
	case p.Delta < -m:
		return fmt.Sprintf("%d pts under pace (%d%% %s) -- banked headroom", rb.RoundToInt(-p.Delta), el, frame)
	default:
		return fmt.Sprintf("on pace (~%d%% %s)", el, frame)
	}
}

// --- formatters (shared) ---

// USD renders a dollar amount with thousands separators: 1234 -> "$1,234" (Ruby CCPool.usd).
func USD(n float64) string { return "$" + commaInt(rb.RoundToInt(n)) }

// USDk abbreviates past a grand: "$1.2k" / "$47" (Ruby CCPool.usdk).
func USDk(n float64) string {
	if n >= 1000 {
		return "$" + rb.Fmt1(n/1000) + "k"
	}
	return fmt.Sprintf("$%d", rb.RoundToInt(n))
}

// At renders an epoch as "Mon 07-03 14:30" (weekday + date + clock time).
func At(epoch int64) string {
	t := time.Unix(epoch, 0).Local()
	return t.Format("Mon 01-02") + " " + clock.Time(t)
}

func commaInt(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(s[i])
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
