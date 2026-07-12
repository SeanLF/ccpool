// Package status holds the two READOUT commands: `status` (the full weekly-pool readout) and `check`
// (time + budget + a keep-going/stop VERDICT). Both are ON-DEMAND: unlike the fail-open hot path
// they report loudly, but `check` mirrors the Ruby contract of returning [lines, code] and never
// raising out (a panic -> [error-line, 2]). They read the per-session snapshots the statusline
// writes and the burn history, and delegate every dollar to ccusage via internal/calib.
package status

import (
	"strconv"

	"github.com/SeanLF/ccpool/internal/burn"
	"github.com/SeanLF/ccpool/internal/calib"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/report"
	"github.com/SeanLF/ccpool/internal/runway"
	"github.com/SeanLF/ccpool/internal/store"
)

// Status is the full `ccpool status` readout: the lines a caller prints (one per element). The
// 3-tier weekly read + $ value + pace + reset-robust burn projection + working-hours runway, then
// the 5h-session nudge when it applies. (The old file-clutter "cleanup" nudges are gone: DB snapshots
// don't litter the filesystem, and the history-size nudge stat'd a JSONL ccpool no longer writes.)
func Status(now int64) []string {
	// One store open for the whole readout: the weekly resolve, calibration, burn envelope, and the
	// 5h snapshot read all share it (fail open -- a nil/non-OK store degrades each to no-data).
	s, sSt := store.Open()
	if s != nil {
		defer s.Close()
	}

	wk, ok := report.ResolveWeekly(s, now)
	if !ok {
		// A non-OK store (locked/corrupt) is NOT a fresh install -- don't tell a user whose DB is
		// unreadable to wire up something already wired. Now that calibration shares the DB with
		// snapshots, one bad store wipes the whole readout, so name it truthfully (as `check` does).
		if sSt != store.StateOK {
			return []string{absentOrCorrupt(sSt)}
		}
		return []string{
			"weekly pool: no data yet. Wire `ccpool statusline` as your Claude Code",
			"statusLine command (settings.json) so it can capture rate_limits, then use CC once.",
		}
	}

	used := wk.Used
	dpp, dppOK := calib.DollarPerPct(s, now, false)
	dollars := "  ·  ($ value calibrating -- needs ccusage + a few days of history)"
	if dppOK {
		dollars = "  ·  ~" + report.USD((100-used)*dpp) + " left of ~" + report.USD(100*dpp) + " (API-equiv)"
	}

	lines := []string{
		"Weekly pool  ·  " + strconv.Itoa(rb.RoundToInt(used)) + "% used" + dollars +
			"  ·  resets " + report.At(wk.Reset) + " (" + fmtx.Dur(wk.Reset-now) + ")" + wk.Stamp(),
		"Pace         ·  " + report.PacePhrase(pool.GetPace(used, wk.Reset, now)),
	}

	// Burn projection (reset-robust). The store window query collapses the raw multi-session log into
	// the monotonic current-window series project() needs (else it sees phantom resets from concurrency).
	var pr burn.Projection
	hasPr := false
	if sSt == store.StateOK {
		if wkHist, st := burn.WeeklyEnvelope(s, now); st == store.StateOK {
			pr, hasPr = burn.Project(wkHist)
		}
	}
	if hasPr {
		// cap from the FRESH used (same basis Runway uses), not the last log sample.
		capH := (100.0 - used) / pr.BurnPerH
		dcap := capH / 24.0
		dreset := float64(wk.Reset-now) / 86400.0
		verdict := "resets first (in " + rb.Fmt1(dreset) + "d) -- you're clear"
		if capH*3600 < float64(wk.Reset-now) {
			verdict = "⚠ ~" + rb.Fmt1(dreset-dcap) + "d BEFORE reset -- you'll throttle early"
		}
		lines = append(lines, "Burn         ·  ~"+rb.Fmt1(pr.BurnPerH)+"%/h -> hits cap in ~"+rb.Fmt1(dcap)+"d; "+verdict)

		if r, ok := runway.Estimate(used, wk.Reset, pr, hasPr, now); ok {
			lines = append(lines, "Runway       ·  "+runway.Phrase(r, wk.Reset-now))
		}
	}

	if g, ok := cutoverGuard(s); ok {
		lines = append(lines, g)
	}

	snaps := pool.LoadSnapshots(s)
	if fh, ok := pool.GetWindow(snaps, "five_hour", now, 6*3600); ok {
		age, _ := pool.DataAge(snaps, now) // 0 when none, matching Ruby `fh[:age] || 0`
		if age <= pool.Stale() && fh.Used >= 70 {
			lines = append(lines, "5h window    ·  "+strconv.Itoa(rb.RoundToInt(fh.Used))+
				"% used (resets "+fmtx.Dur(fh.Reset-now)+") -- session throttle near")
		}
	}

	return lines
}
