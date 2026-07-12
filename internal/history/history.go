// Package history appends the rate-limit history (weekly/5h used% + resets) that calibration and burn
// projection read. Records go to the SQLite store (internal/store); the append is per-session
// deduped and 5h-throttled so a per-render statusline does not bloat the log. Best-effort throughout:
// no history is never fatal, and nothing here panics (it runs on the statusline hot path).
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

// Seed appends a history row for this render. No-op (returns nil) unless the payload carries a numeric
// seven_day used_percentage. Dedup + throttle drop redundant rows; the sanity guard drops a row whose
// reset is absurd. Fail-open: a non-OK store or a read failure skips the write, never crashes.
func Seed(payload map[string]any, now int64) error {
	rl, ok := payload["rate_limits"].(map[string]any)
	if !ok {
		return nil
	}
	sd, ok := rl["seven_day"].(map[string]any)
	if !ok {
		return nil
	}
	wk, ok := rb.Num(sd["used_percentage"])
	if !ok {
		return nil
	}

	r := store.HistoryRow{
		T:       now,
		Wk:      wk,
		WkReset: intPtr(sd["resets_at"]),
		Tier:    tier(),
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

	// Fail-open: a non-OK store or unreadable last row skips this render's write, never crashes. The
	// user-facing signal for a persistently broken/unwritable DB is the loud status/check commands
	// (Task 10 surfaces StateCorrupt/StateTransient); the statusline hot path stays silent by design.
	s, st := store.Open()
	if st != store.StateOK {
		return nil
	}
	defer s.Close()

	last, lst := s.LastSessionRow(r.Session)
	if lst != store.StateOK {
		return nil // can't read the last row to dedup -> skip rather than risk a bad append
	}
	if skip(last, r, now) {
		return nil
	}
	return s.AppendHistory(r)
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

func costPtr(payload map[string]any) *float64 {
	c, ok := payload["cost"].(map[string]any)
	if !ok {
		return nil
	}
	return floatPtr(c["total_cost_usd"])
}

func tier() string {
	return env.String("USAGE_TIER", "max_20x")
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
