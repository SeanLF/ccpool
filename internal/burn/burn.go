// Package burn projects weekly and 5h burn rates from the rate-limit history log the statusline
// records (~/.claude/rate-limit-history.jsonl). Reset-robust by design: a DROP in % (or a change of
// reset window) is treated as a reset boundary, so a surprise Anthropic reset can never produce a
// phantom negative burn or a doom projection — it just restarts the clock. Flat / just-reset history
// yields no projection, not a guess. Fail-soft: Read never panics and distinguishes an UNREADABLE /
// garbled history (readable=false) from a merely absent / empty one (readable=true, empty slice).
package burn

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/SeanLF/ccpool/internal/rb"
)

// Entry is one history sample (or an envelope row): the raw parsed object, numbers kept as
// json.Number for read rows and float64 for envelope-built rows. Read numeric fields via num/tof.
type Entry = map[string]any

// Noise guards. Weekly % is a coarse integer, so it wiggles +-1 between renders; without these a
// 1-point blip over two minutes extrapolates to an absurd rate.
const (
	dropReset = 5.0 // a wk fall bigger than this = a real reset, not noise
	minSpanH  = 2.0 // need this many hours of run before a slope is trustworthy
	minDelta  = 3.0 // and this much net climb (else it's within rounding noise)

	// Short-horizon (5h SESSION) least-squares fit knobs.
	recentSecs     = 1800.0 // trailing window to fit (30 min)
	recentMinSpanH = 0.08   // ~5 min of run before a slope is trustworthy
	recentMinDelta = 2.0    // and this much climb (past integer noise)

	keepSecs = 14 * 86400 // drop samples older than this on read

	// resetJitter is how far below the max a reset may sit and still count as the SAME window.
	// Anthropic's reported resets_at wobbles a few SECONDS between renders, so a strict max lands on
	// a 1-sample outlier and collapses the envelope (burn/runway silently die). A window is >= 5h
	// away from its neighbour, so 300s can't merge two real windows.
	resetJitter = 300.0
)

// Projection is the weekly burn result: avg %/hour over the current run and the derived time-to-cap,
// plus the run bounds Runway re-measures in working hours.
type Projection struct {
	BurnPerH   float64
	HoursToCap float64
	Wk         float64
	FirstT     int64
	LastT      int64
	Dpct       float64
}

// Recent is the short-horizon (5h) fit: %/hour, time-to-cap, and the last value.
type Recent struct {
	RatePerH   float64
	HoursToCap float64
	Val        float64
}

// num reads a value as a float64, reporting whether it was numeric (mirrors Ruby is_a?(Numeric)).
// Handles both json.Number (read rows) and float64 (envelope-built rows).
func num(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// tof mirrors Ruby's numeric .to_f: the value if numeric, else 0.0 (nil.to_f == 0.0).
func tof(v any) float64 { f, _ := num(v); return f }

// Read returns recent, well-formed samples (chronological). readable=false is the Ruby `nil`: the
// file exists with content but nothing parses (drift/corruption) or it can't be read at all. An
// absent or empty file returns (empty, true). Never panics.
func Read(path string, now int64) (entries []Entry, readable bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Entry{}, true // absent -> []
		}
		return nil, false // I/O error (EACCES, EISDIR, ...) -> unreadable
	}

	var parsed []Entry
	anyContent := false
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			anyContent = true
		}
		if e := parse(l); e != nil {
			parsed = append(parsed, e)
		}
	}
	// Content present but not a single usable line -> drift/corruption, not warmup.
	if len(parsed) == 0 && anyContent {
		return nil, false
	}

	cutoff := now - keepSecs
	kept := []Entry{} // non-nil, matching the absent-file branch's ([]Entry{}, true) contract
	for _, e := range parsed {
		if t, ok := num(e["t"]); ok && int64(t) >= cutoff {
			kept = append(kept, e)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return tof(kept[i]["t"]) < tof(kept[j]["t"]) })
	return kept, true
}

// parse validates one line: a JSON object with numeric t and wk. A bad line is dropped (nil), not
// fatal; Read flags an all-bad, non-empty file as unreadable.
func parse(line string) Entry {
	e := rb.ParseObject([]byte(line))
	if e == nil {
		return nil
	}
	if _, ok := num(e["t"]); !ok {
		return nil
	}
	if _, ok := num(e["wk"]); !ok {
		return nil
	}
	return e
}

// Envelope collapses the raw multi-session log into ONE account-global monotonic series for a single
// field over its CURRENT window: the running MAX over the latest reset window (older windows are
// prior weeks/sessions and are dropped). Returns nil when entries is nil, so an unreadable history
// stays distinguishable from an empty one.
func Envelope(entries []Entry, field, resetField string) []Entry {
	if entries == nil {
		return nil
	}
	// latest reset among rows where both field and reset are numeric (non-numeric resets can't be
	// ordered and are excluded; the loop's `== latest` then drops them too).
	var latest float64
	hasLatest := false
	for _, e := range entries {
		_, fok := num(e[field])
		rv, rok := num(e[resetField])
		if fok && rok {
			if !hasLatest || rv > latest {
				latest = rv
			}
			hasLatest = true
		}
	}

	var out []Entry
	var running float64
	haveRunning := false
	for _, e := range entries {
		fv, fok := num(e[field])
		if !fok {
			continue
		}
		if hasLatest {
			rv, rok := num(e[resetField])
			if !rok || latest-rv > resetJitter {
				continue // current window only (bucketing jittered resets within tolerance of the max)
			}
		} else if e[resetField] != nil {
			continue // no reset recorded anywhere: only rows with a nil reset pass (nil == nil)
		}
		if !haveRunning || fv > running {
			running = fv
		}
		haveRunning = true
		row := Entry{"t": e["t"], field: running}
		if hasLatest {
			row[resetField] = latest
		}
		out = append(out, row)
	}
	return out
}

// currentRun is the longest trailing run since the most recent reset boundary. A reset is a BIG drop
// (> dropReset) or a change of reset window; small -1 dips stay inside the run.
func currentRun(entries []Entry) []Entry {
	if len(entries) < 2 {
		return entries
	}
	start := len(entries) - 1
	for i := len(entries) - 1; i >= 1; i-- {
		if tof(entries[i]["wk"]) < tof(entries[i-1]["wk"])-dropReset {
			break // weekly fell hard -> a reset landed here
		}
		if resetDiffers(entries[i]["wk_reset"], entries[i-1]["wk_reset"]) {
			break // window changed -> new week
		}
		start = i - 1
	}
	return entries[start:]
}

// resetDiffers mirrors Ruby `a != b` for reset values: both nil -> equal, numeric-equal -> equal.
func resetDiffers(a, b any) bool {
	av, aok := num(a)
	bv, bok := num(b)
	if aok != bok {
		return true
	}
	if aok {
		return av != bv
	}
	return false
}

// Project is the average %/hour over the current run and the resulting time-to-cap. ok=false when
// there isn't enough signal (no entries, flat, or just reset).
func Project(entries []Entry) (Projection, bool) {
	if len(entries) < 2 {
		return Projection{}, false
	}
	run := currentRun(entries)
	if len(run) < 2 {
		return Projection{}, false
	}
	first, last := run[0], run[len(run)-1]
	ft, lt := tof(first["t"]), tof(last["t"])
	dtH := (lt - ft) / 3600.0
	dpct := tof(last["wk"]) - tof(first["wk"])
	// Need a real climb over a real span: too little of either and the slope is noise.
	if dtH < minSpanH || dpct < minDelta {
		return Projection{}, false
	}
	burn := dpct / dtH
	return Projection{
		BurnPerH:   burn,
		HoursToCap: (100.0 - tof(last["wk"])) / burn,
		Wk:         tof(last["wk"]),
		FirstT:     int64(ft),
		LastT:      int64(lt),
		Dpct:       dpct,
	}, true
}

// trailingRun is the trailing run of a field since its last reset (a drop > dropReset). Detects a
// session reset purely from a drop in value (no reset-window column for ses).
func trailingRun(entries []Entry, field string) []Entry {
	if len(entries) < 2 {
		return entries
	}
	start := len(entries) - 1
	for i := len(entries) - 1; i >= 1; i-- {
		if tof(entries[i][field]) < tof(entries[i-1][field])-dropReset {
			break
		}
		start = i - 1
	}
	return entries[start:]
}

// lsqSlope is the ordinary least-squares slope (field-% per hour) over a run. ok=false when the
// x-variance is zero.
func lsqSlope(run []Entry, field string) (float64, bool) {
	n := float64(len(run))
	t0 := tof(run[0]["t"])
	xs := make([]float64, len(run))
	ys := make([]float64, len(run))
	var sx, sy float64
	for i, e := range run {
		xs[i] = (tof(e["t"]) - t0) / 3600.0
		ys[i] = tof(e[field])
		sx += xs[i]
		sy += ys[i]
	}
	mx, my := sx/n, sy/n
	var den float64
	for _, x := range xs {
		den += (x - mx) * (x - mx)
	}
	if den == 0 {
		return 0, false
	}
	var numr float64
	for i := range xs {
		numr += (xs[i] - mx) * (ys[i] - my)
	}
	return numr / den, true
}

// ProjectRecent is the short-horizon projection for a fast window: fit the trailing recentSecs of
// samples carrying field and extrapolate to 100%. ok=false when there's no real, positive climb over
// a real span.
func ProjectRecent(entries []Entry, now int64, field string) (Recent, bool) {
	if len(entries) == 0 {
		return Recent{}, false
	}
	var recent []Entry
	for _, e := range entries {
		if t, ok := num(e["t"]); ok && int64(t) >= now-int64(recentSecs) {
			if _, ok := num(e[field]); ok {
				recent = append(recent, e)
			}
		}
	}
	if len(recent) < 3 {
		return Recent{}, false
	}
	run := trailingRun(recent, field)
	if len(run) < 3 {
		return Recent{}, false
	}
	spanH := (tof(run[len(run)-1]["t"]) - tof(run[0]["t"])) / 3600.0
	delta := tof(run[len(run)-1][field]) - tof(run[0][field])
	if spanH < recentMinSpanH || delta < recentMinDelta {
		return Recent{}, false
	}
	slope, ok := lsqSlope(run, field)
	if !ok || slope <= 0 {
		return Recent{}, false
	}
	last := tof(run[len(run)-1][field])
	return Recent{RatePerH: slope, HoursToCap: (100.0 - last) / slope, Val: last}, true
}
