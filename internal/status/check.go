package status

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/SeanLF/ccpool/internal/burn"
	"github.com/SeanLF/ccpool/internal/clock"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/profile"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/runway"
	"github.com/SeanLF/ccpool/internal/store"
)

// Report is `ccpool check`: time + remaining budget + a keep-going/stop VERDICT. Returns
// [lines, exit_code]: 0 = report, 2 = no data. On-demand, so it always reports (with a staleness
// caveat) rather than staying silent on old data. Never raises out — a panic becomes [error, 2].
func Report(now int64) (lines []string, code int) {
	defer func() {
		if r := recover(); r != nil {
			lines = []string{fmt.Sprintf("ccpool check: %v", r)}
			code = 2
		}
	}()

	// One store open serves both the snapshots and the burn envelope (they share the DB now). A non-OK
	// store means both are unreadable -- there is no "snapshots readable, history not" split anymore.
	s, sSt := store.Open()
	if s != nil {
		defer s.Close()
	}
	snaps, snapSt := readSnapshots(s, sSt)
	if len(snaps) == 0 {
		return []string{absentOrCorrupt(snapSt)}, 2
	}

	age, ageOK := pool.DataAge(snaps, now)
	ses, sesOK := pool.GetWindow(snaps, "five_hour", now, 6*3600)
	wk, wkOK := pool.GetWindow(snaps, "seven_day", now, pool.Week+86400)

	var wkHist, sesHist []burn.Entry
	histState := sSt
	if sSt == store.StateOK {
		var wkSt, sesSt store.ReadState
		wkHist, wkSt = burn.WeeklyEnvelope(s, now)
		sesHist, sesSt = burn.FiveHourEnvelope(s, now)
		histState = worseState(wkSt, sesSt)
	}

	t := time.Unix(now, 0).Local()
	out := []string{
		"time     " + t.Format("2006-01-02") + " " + clock.Time(t) + " " + t.Format("MST (Mon)"),
		"data     " + freshness(age, ageOK),
		"",
	}
	if g, ok := cutoverGuard(s); ok {
		out = append(out, g, "")
	}
	sesSoon := sessionLines(&out, ses, sesOK, sesHist, now)
	paceWarn := weeklyLines(&out, wk, wkOK, wkHist, histState, now)
	out = append(out, "")
	out = append(out, "VERDICT  "+verdict(ses, sesOK, wk, wkOK, sesSoon, paceWarn, now))
	return out, 0
}

// readSnapshots reads the parsed snapshot maps from the already-open store, returning the read state
// so the caller can tell warm-up (StateOK, empty) from an unreadable store. A nil or non-OK store
// short-circuits to no snapshots carrying that state (both subsystems share the one DB's fate now).
func readSnapshots(s *store.Store, sSt store.ReadState) ([]map[string]any, store.ReadState) {
	if s == nil || sSt != store.StateOK {
		return nil, sSt
	}
	return s.Snapshots()
}

// absentOrCorrupt is check's no-data message, keyed off the store read state (snapshots live in the DB
// now, not globbed files). StateOK with zero rows is the warm-up case; a non-OK store is genuinely
// unreadable -- and we keep the truthful busy-vs-corrupt split (transient -> retry, not a false
// corruption alarm). Payloads are valid JSON by construction, so "rows present but none parse" folds
// into warm-up; Open self-heals real corruption to an empty StateOK DB, making StateCorrupt rare here.
func absentOrCorrupt(st store.ReadState) string {
	switch st {
	case store.StateTransient:
		return "Usage data is temporarily unreadable -- the database is busy or unreachable. Treat budget as\n" +
			"unknown; don't guess. This is usually transient; re-run in a moment."
	case store.StateCorrupt:
		return "The usage database is unreadable (corrupt). Treat budget as unknown; don't guess. A fresh\n" +
			"statusline render rebuilds it; re-run once an interactive window has redrawn."
	default:
		return "No usage snapshots yet. The statusline writes one per session on every render, so this is\n" +
			"empty only if no interactive Claude Code window has drawn on this machine recently (e.g. a\n" +
			"pure background job with no TUI). Open/refresh an interactive window, then re-run. Don't guess."
	}
}

// cutoverGuard fires the one-time "history not imported" warning: a legacy rate-limit-history.jsonl
// exists but the store's history table is empty, so the user upgraded without running the importer.
// Reuses LastSessionRow(nil) for emptiness (no extra query); silent on a fresh install (no JSONL).
func cutoverGuard(s *store.Store) (string, bool) {
	if s == nil {
		return "", false
	}
	last, st := s.LastSessionRow(nil)
	if st != store.StateOK || last != nil {
		return "", false // unreadable, or history already has rows -> no guard
	}
	if fi, err := os.Stat(paths.History()); err == nil && fi.Size() > 0 {
		return "history  ·  " + paths.History() + " has rows but the database is empty --\n" +
			"         run the one-off importer to restore weekly burn/runway", true
	}
	return "", false
}

// worseState reports the more-severe of two read states (Corrupt > Transient > OK) so one history
// message reflects the worst of the weekly/5h reads.
func worseState(a, b store.ReadState) store.ReadState {
	if a == store.StateCorrupt || b == store.StateCorrupt {
		return store.StateCorrupt
	}
	if a == store.StateTransient || b == store.StateTransient {
		return store.StateTransient
	}
	return store.StateOK
}

func freshness(age int64, ageOK bool) string {
	if !ageOK {
		return "unknown age"
	}
	if age <= 90 {
		return "fresh (" + strconv.FormatInt(age, 10) + "s ago)"
	}
	if age <= env.Int64("CCPOOL_CHECK_STALE_SECS", 900) {
		return fmtx.Dur(age) + " old"
	}
	return "STALE -- " + fmtx.Dur(age) + " old; statusline not rendering. The real budget may have moved."
}

// sessionLines appends the 5h SESSION block. Returns ses_soon: the projected 5h cap is imminent even
// if the window isn't yet >= SESSION_FULL.
func sessionLines(lines *[]string, ses pool.Window, sesOK bool, sesHist []burn.Entry, now int64) bool {
	if !sesOK {
		*lines = append(*lines, "SESSION  (no live 5h window across sessions -- all snapshots predate the last reset)")
		return false
	}
	*lines = append(*lines, fmt.Sprintf("SESSION  %d%% used  ·  resets in %s  (5h window)", rb.RoundToInt(ses.Used), fmtx.Dur(ses.Reset-now)))

	sp, ok := burn.ProjectRecent(sesHist, now, "ses")
	if !ok {
		return false
	}
	capIn := sp.HoursToCap * 3600
	rate := fmt.Sprintf("%.1f%%/h", sp.RatePerH)
	if sp.RatePerH >= 60 {
		rate = fmt.Sprintf("%.1f%%/min", sp.RatePerH/60.0)
	}
	if float64(ses.Reset-now) <= capIn {
		*lines = append(*lines, fmt.Sprintf("         burn: ~%s -> window resets (in %s) before you'd cap, fine", rate, fmtx.Dur(ses.Reset-now)))
		return false
	}
	*lines = append(*lines, fmt.Sprintf("         burn: ~%s -> 5h cap in ~%s at this rate; land work before the pause", rate, fmtx.Dur(int64(capIn))))
	return capIn <= float64(env.Int64("CCPOOL_CHECK_SES_SOON_SECS", 900))
}

// weeklyLines appends the WEEKLY block. Returns pace_warn: past the linear share and not near reset.
func weeklyLines(lines *[]string, wk pool.Window, wkOK bool, wkHist []burn.Entry, histState store.ReadState, now int64) bool {
	if !wkOK {
		*lines = append(*lines, "WEEKLY   (no live 7d window across sessions -- snapshots missing or stale)")
		return false
	}
	used, reset := wk.Used, wk.Reset
	*lines = append(*lines, fmt.Sprintf("WEEKLY   %d%% used  ·  resets in %s  (7d window)", rb.RoundToInt(used), fmtx.Dur(reset-now)))

	p := pool.GetPace(used, reset, now)
	paceWarn := p.Delta > env.Float("CCPOOL_PACE_MARGIN", 3) && p.ToReset > env.Int64("CCPOOL_COAST_SECS", 43200)
	daysLeft := math.Max(float64(reset-now)/86400.0, 0.0001)
	remaining := math.Max(100.0-used, 0.0)
	todayCap := clampF(math.Min(remaining, remaining/daysLeft), 0, 100)
	even := profile.Load().Uniform()
	word := "your work-rhythm pace"
	if even {
		word = "even-burn pace"
	}

	var note string
	switch {
	case math.Abs(p.Delta) < 2:
		note = "on " + word
	case p.Delta > 0:
		note = fmt.Sprintf("%dpts AHEAD of %s (burning fast)", rb.RoundToInt(p.Delta), word)
	default:
		// the 24/7 caveat only applies to `even`; a schedule profile already accounts for idle.
		tail := ""
		if even {
			tail = " -- expected unless you run 24/7 (idle/sleep counts as elapsed)"
		}
		note = fmt.Sprintf("%dpts UNDER %s%s", rb.RoundToInt(-p.Delta), word, tail)
	}
	label := "of " + word
	if even {
		label = "of week elapsed"
	}
	*lines = append(*lines, fmt.Sprintf("         %d%% %s -> %s", rb.RoundToInt(p.ElapsedPct), label, note))
	*lines = append(*lines, fmt.Sprintf("         pace guide: ~%d%%/day spends the rest evenly to reset (not a hard cap)", rb.RoundToInt(todayCap)))

	proj, hasProj := burn.Project(wkHist)
	if hasProj {
		secsToCap := proj.HoursToCap * 3600
		if secsToCap >= float64(reset-now) {
			*lines = append(*lines, fmt.Sprintf("         burn: ~%.1f%%/h -> even non-stop, resets before you'd reach the cap, fine", proj.BurnPerH))
		} else {
			idle := float64(reset-now) - secsToCap
			tail := "just shy of reset -> burn it down freely"
			if idle > env.Float("CCPOOL_CHECK_IDLE_WARN_H", 24)*3600 {
				tail = "~" + fmtx.Dur(int64(idle)) + " idle before reset -- ease off IF you'll sustain this unattended"
			}
			*lines = append(*lines, fmt.Sprintf("         burn: ~%.1f%%/h; IF sustained 24/7, cap in ~%s -- %s (idle/sleep stretches this out)", proj.BurnPerH, fmtx.Dur(int64(secsToCap)), tail))
		}
	} else {
		switch histState {
		case store.StateCorrupt:
			*lines = append(*lines, "         burn: history unreadable -- projection unavailable (not a clear signal)")
		case store.StateTransient:
			*lines = append(*lines, "         burn: history read failed -- projection unknown, retry (a busy or unreachable database)")
		}
	}
	if r, ok := runway.Estimate(used, reset, proj, hasProj, now); ok {
		*lines = append(*lines, "         runway: "+runway.Phrase(r, reset-now))
	}
	return paceWarn
}

// verdict is the keep-going/stop decision. The trap (learned the hard way) is treating a near-full
// 5h SESSION window as "out of budget" and stopping — it resets in hours; only the WEEKLY pool
// genuinely low is a real "stop for the week".
func verdict(ses pool.Window, sesOK bool, wk pool.Window, wkOK bool, sesSoon, paceWarn bool, now int64) string {
	sessionFull := env.Float("CCPOOL_CHECK_SES_FULL", 92)
	weeklyLow := env.Float("CCPOOL_CHECK_WEEKLY_LOW", 90)
	coast := env.Int64("CCPOOL_COAST_SECS", 43200)

	wkLeft := 0
	if wkOK {
		wkLeft = rb.RoundToInt(math.Max(100.0-wk.Used, 0))
	}

	switch {
	case sesOK && (ses.Used >= sessionFull || sesSoon) && (!wkOK || wk.Used < weeklyLow):
		left := "budget"
		if wkOK {
			left = strconv.Itoa(wkLeft) + "%"
		}
		rst := " (resets in " + fmtx.Dur(ses.Reset-now) + ")"
		state := "on pace to hit the 5h cap soon at your current burn"
		if ses.Used >= sessionFull {
			state = "5h window almost full"
		}
		return "SESSION-LIMITED -- " + state + rst + ". TEMPORARY: land in-flight work, then pause and resume after " +
			"the session resets. Do NOT call the work done while " + left + " of the weekly pool remains."
	case wkOK && wk.Used >= weeklyLow && (wk.Reset-now) <= coast:
		return "COAST -- weekly is nearly spent (" + strconv.Itoa(wkLeft) + "% left) but it resets in " + fmtx.Dur(wk.Reset-now) + ". " +
			"Unspent budget is use-it-or-lose-it, so spend the rest freely."
	case wkOK && wk.Used >= weeklyLow:
		return "WIND DOWN -- weekly pool is nearly spent (" + strconv.Itoa(wkLeft) + "% left). Land what's in flight and stop for the " +
			"week. Finish the task if that's cheaper than a handover; otherwise stop at a natural boundary and " +
			"checkpoint properly -- update docs and leave a handover note so the next session resumes cheaply."
	case wkOK && wk.Used < weeklyLow && burnDown(wk, wkLeft, now):
		lost := rb.RoundToInt(forfeit(wk, wkLeft, now))
		return "BURN DOWN -- " + strconv.Itoa(wkLeft) + "% unspent, reset in " + fmtx.Dur(wk.Reset-now) + "; at your ~1/7/day pace ~" + strconv.Itoa(lost) + "% " +
			"would go UNSPENT and reset to zero (you can't bank it). If you have valuable but deferrable work -- deep " +
			"passes, parallel fan-outs, research, an overnight loop -- spend it now: go bigger/parallel. Not busywork, " +
			"but don't waste the headroom."
	case !wkOK && !sesOK:
		return "UNKNOWN -- no live budget data across snapshots. Don't guess; re-render the statusline."
	case paceWarn:
		head := "weekly headroom"
		if wkOK {
			head = strconv.Itoa(wkLeft) + "% weekly headroom"
		}
		return "PACE DOWN -- " + head + ", but you're past the linear share of the week. Front-loaded, not doomed -- it " +
			"self-corrects for interactive work. Before a big new thread, spread the spend; finish or checkpoint " +
			"what's in flight first."
	default:
		// Only ONE window may be nil here (both-nil is UNKNOWN above). Don't read an ABSENT window as
		// "healthy" -- report it as unknown, matching check's no-false-all-clear rule.
		wkp := "weekly unknown (no live window -- re-render)"
		if wkOK {
			wkp = strconv.Itoa(wkLeft) + "% weekly headroom"
		}
		sesp := "session unknown (no live window)"
		if sesOK {
			sesp = "session has room"
		}
		return "KEEP GOING -- " + wkp + ", " + sesp + ". Spend the budget you were asked to spend."
	}
}

// forfeit is the unspent-at-reset headroom: at the even ~1/7-per-day pace, how much of wk_left would
// still be on the table when the week resets.
func forfeit(wk pool.Window, wkLeft int, now int64) float64 {
	daysToReset := math.Max(float64(wk.Reset-now)/86400.0, 0)
	return float64(wkLeft) - daysToReset*(100.0/7.0)
}

// burnDown fires when a meaningful chunk would go unspent -> nudge to spend it.
func burnDown(wk pool.Window, wkLeft int, now int64) bool {
	return forfeit(wk, wkLeft, now) >= env.Float("CCPOOL_CHECK_BURNDOWN_FORFEIT", 15)
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
