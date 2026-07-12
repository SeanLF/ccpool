// Package history builds the rate-limit history rows (weekly/5h used% + resets) that calibration and
// burn projection read, and decides per-session dedup + 5h throttle so a per-render statusline does
// not bloat the log. The statusline capture writes the row -- alongside the session snapshot in one
// store transaction. Best-effort: nothing here panics (it runs on the statusline hot path).
package history

import (
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

// minIntervalDefault throttles 5h-only writes (wk flat) to curb log growth.
const minIntervalDefault = 60

// maxResetAhead bounds a sane reset epoch. A single far-future reset (e.g. the 9999999999 sentinel
// found in live data) would become the weekly/5h envelope's latest = max(reset) and collapse the
// window, and skew calibration's reset grouping, since burn/calib -- unlike pool.GetWindow -- trust
// the reset with no upper bound. Legit weekly/5h resets are always within ~7d/5h ahead, so 30d is a
// wide "clearly absurd" cutoff that never false-rejects. Only the far-future case poisons (max picks
// it); a past reset is inert (never the max, and the 300s jitter bucket drops it), so we do not bound
// below -- a lower bound would needlessly drop legit rows at every 5h reset boundary.
const maxResetAhead = 30 * 86400

// Prepare builds this render's history row and reports whether it should be appended: false when the
// payload has no numeric seven_day used%, when dedup/throttle drops it, or when the last row can't be
// read. The caller (statusline capture) has already opened the store and writes the returned row with
// the snapshot in one transaction. The sanity guard nulls an absurd (far-future) reset per window.
func Prepare(s *store.Store, payload map[string]any, now int64) (store.HistoryRow, bool) {
	rl, ok := payload["rate_limits"].(map[string]any)
	if !ok {
		return store.HistoryRow{}, false
	}
	sd, ok := rl["seven_day"].(map[string]any)
	if !ok {
		return store.HistoryRow{}, false
	}
	wk, ok := rb.Num(sd["used_percentage"])
	if !ok {
		return store.HistoryRow{}, false
	}

	r := store.HistoryRow{
		T:       now,
		Wk:      wk,
		WkReset: intPtr(sd["resets_at"]),
		Cost:    costPtr(payload),
		Session: sessionPtr(payload),
	}
	if fh, ok := rl["five_hour"].(map[string]any); ok {
		r.Ses = floatPtr(fh["used_percentage"])
		r.SesReset = intPtr(fh["resets_at"])
	}

	// Sanity guard: null just the offending reset, per window, rather than drop the whole row. A
	// far-future reset would otherwise become the envelope's latest=max(reset) and collapse the
	// window; nulling it excludes the row from THAT window's envelope (and calibration's reset
	// grouping) while preserving the other window's good sample -- weekly keys off wk_reset only, 5h
	// off ses_reset only. This keeps the 9999999999 sentinel bug from recurring on live ingest.
	if r.WkReset != nil && *r.WkReset > now+maxResetAhead {
		r.WkReset = nil
	}
	if r.SesReset != nil && *r.SesReset > now+maxResetAhead {
		r.SesReset = nil
	}

	// Fail-open: an unreadable last row skips the append (snapshot-only render), never crashes. The
	// user-facing signal for a persistently broken/unwritable DB is the loud status/check commands
	// (Task 10 surfaces StateCorrupt/StateTransient); the statusline hot path stays silent by design.
	last, lst := s.LastSessionRow(r.Session)
	if lst != store.StateOK {
		return r, false
	}
	return r, !skip(last, r, now)
}

// skip reports whether dedup/throttle drops this row: if the last row for this session has the same
// weekly % and reset, drop it when the 5h % also matches (nothing moved) or when only the 5h % moved
// but we wrote too recently (throttle). Typed comparison over the stored values -- the retired Ruby
// numEqual/json.Number dance is gone (float64 equality is identical, epochs round-trip exactly).
func skip(last *store.HistoryRow, r store.HistoryRow, now int64) bool {
	if last == nil {
		return false
	}
	if last.Wk != r.Wk || !int64PtrEq(last.WkReset, r.WkReset) {
		return false
	}
	if float64PtrEq(last.Ses, r.Ses) {
		return true // nothing moved
	}
	return now-last.T < int64(minInterval()) // only the 5h % moved -> throttle
}

// --- helpers ---

// intPtr / floatPtr read a payload field (json.Number via rb.Num) as an *int64 / *float64, nil when
// absent or non-numeric. resets_at is an integer epoch (< 2^53, so int64(f) is exact).
func intPtr(v any) *int64 {
	if f, ok := rb.Num(v); ok {
		i := int64(f)
		return &i
	}
	return nil
}

func floatPtr(v any) *float64 {
	if f, ok := rb.Num(v); ok {
		return &f
	}
	return nil
}

func sessionPtr(payload map[string]any) *string {
	if s, ok := payload["session_id"].(string); ok {
		return &s
	}
	return nil
}

// costPtr reads the CC payload's cost.total_cost_usd (Claude Code's own session cost, a CC input we
// store though nothing reads it yet). nil when absent or non-numeric.
func costPtr(payload map[string]any) *float64 {
	c, ok := payload["cost"].(map[string]any)
	if !ok {
		return nil
	}
	return floatPtr(c["total_cost_usd"])
}

func minInterval() int {
	return env.Int("CCPOOL_HISTORY_MIN_INTERVAL", minIntervalDefault)
}

func int64PtrEq(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b // both-nil equal; one-nil unequal
	}
	return *a == *b
}

func float64PtrEq(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
