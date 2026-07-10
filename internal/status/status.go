// Package status holds the two READOUT commands: `status` (the full weekly-pool readout) and `check`
// (time + budget + a keep-going/stop VERDICT). Both are ON-DEMAND: unlike the fail-open hot path
// they report loudly, but `check` mirrors the Ruby contract of returning [lines, code] and never
// raising out (a panic -> [error-line, 2]). They read the per-session snapshots the statusline
// writes and the burn history, and delegate every dollar to ccusage via internal/calib.
package status

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/SeanLF/ccpool/internal/burn"
	"github.com/SeanLF/ccpool/internal/calib"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/report"
	"github.com/SeanLF/ccpool/internal/runway"
)

// Status is the full `ccpool status` readout: the lines a caller prints (one per element). The
// 3-tier weekly read + $ value + pace + reset-robust burn projection + working-hours runway, then
// the 5h-session and cleanup nudges when they apply.
func Status(now int64) []string {
	wk, ok := report.ResolveWeekly(now)
	if !ok {
		return []string{
			"weekly pool: no data yet. Wire `ccpool statusline` as your Claude Code",
			"statusLine command (settings.json) so it can capture rate_limits, then use CC once.",
		}
	}

	used := wk.Used
	dpp, dppOK := calib.DollarPerPct(now, false)
	dollars := "  ·  ($ value calibrating -- needs ccusage + a few days of history)"
	if dppOK {
		dollars = "  ·  ~" + report.USD((100-used)*dpp) + " left of ~" + report.USD(100*dpp) + " (API-equiv)"
	}

	lines := []string{
		"Weekly pool  ·  " + strconv.Itoa(rb.RoundToInt(used)) + "% used" + dollars +
			"  ·  resets " + report.At(wk.Reset) + " (" + fmtx.Dur(wk.Reset-now) + ")" + wk.Stamp(),
		"Pace         ·  " + report.PacePhrase(pool.GetPace(used, wk.Reset, now)),
	}

	// Burn projection (reset-robust). envelope() first: the raw log interleaves concurrent sessions,
	// so project() needs the collapsed monotonic current-window series or it sees phantom resets.
	entries, readable := burn.Read(paths.History(), now)
	var pr burn.Projection
	hasPr := false
	if readable {
		pr, hasPr = burn.Project(burn.Envelope(entries, "wk", "wk_reset"))
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

	snaps := pool.LoadSnapshots()
	if fh, ok := pool.GetWindow(snaps, "five_hour", now, 6*3600); ok {
		age, _ := pool.DataAge(snaps, now) // 0 when none, matching Ruby `fh[:age] || 0`
		if age <= pool.Stale() && fh.Used >= 70 {
			lines = append(lines, "5h window    ·  "+strconv.Itoa(rb.RoundToInt(fh.Used))+
				"% used (resets "+fmtx.Dur(fh.Reset-now)+") -- session throttle near")
		}
	}

	// Surface (never auto-delete) accumulating snapshots + an oversized history log.
	if n := len(staleCaches(now)); n >= 20 {
		lines = append(lines, "cleanup      ·  "+strconv.Itoa(n)+" stale session snapshots accumulating -- run `ccpool prune` to clean")
	}
	if line, ok := historyCleanup(now); ok {
		lines = append(lines, line)
	}
	return lines
}

// staleCaches lists the per-session snapshot files (and their write-tmp siblings) older than the
// keep window. Surfaced by status when they pile up; matches CCPool.stale_caches.
func staleCaches(now int64) []string {
	keep := env.Int64("CCPOOL_CACHE_KEEP_SECS", 3600)
	glob := paths.SnapshotGlob()
	files, _ := filepath.Glob(glob)
	tmps, _ := filepath.Glob(glob + ".*.tmp")
	var out []string
	for _, f := range append(files, tmps...) {
		info, err := os.Stat(f)
		if err != nil {
			continue // unreadable mtime -> skip (Ruby's `rescue false`)
		}
		if now-info.ModTime().Unix() > keep {
			out = append(out, f)
		}
	}
	return out
}

// historyCleanup surfaces an oversized history log (opt-out via CCPOOL_HISTORY_KEEP_DAYS<=0).
func historyCleanup(now int64) (string, bool) {
	keepDays := env.Float("CCPOOL_HISTORY_KEEP_DAYS", 30)
	if keepDays <= 0 {
		return "", false
	}
	warnMB := env.Float("CCPOOL_HISTORY_WARN_MB", 20)
	size := 0.0
	if info, err := os.Stat(paths.History()); err == nil {
		size = float64(info.Size())
	}
	mb := size / 1048576.0
	if mb > warnMB {
		return "cleanup      ·  usage history is " + strconv.Itoa(rb.RoundToInt(mb)) +
			"MB -- `ccpool prune --history` compacts it to the last " + strconv.Itoa(rb.RoundToInt(keepDays)) + "d", true
	}
	return "", false
}
