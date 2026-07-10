package burn

import "testing"

// Anthropic's reported wk_reset wobbles a few SECONDS between renders (real data: 1784163600,
// 1784163604, 1784163562 within one week). Envelope must treat every row whose reset is within the
// jitter tolerance of the max as the SAME current window; otherwise the strict max lands on a
// 1-sample outlier and the envelope collapses to that one row -> Project returns nil -> burn/runway
// silently die (the A0 bug). Older windows (weeks away) must still be excluded.
func TestEnvelopeBucketsJitteredReset(t *testing.T) {
	const base = 1784163600
	entries := []Entry{
		// a prior week's window -- must be excluded (far below the tolerance band)
		{"t": int64(0), "wk": 90.0, "wk_reset": float64(base - 7*86400)},
		// the real current window: four climbing samples, resets jittering by a few seconds
		{"t": int64(1000), "wk": 10.0, "wk_reset": float64(base)},
		{"t": int64(4600), "wk": 20.0, "wk_reset": float64(base - 38)},
		{"t": int64(8200), "wk": 30.0, "wk_reset": float64(base + 2)},
		{"t": int64(11800), "wk": 40.0, "wk_reset": float64(base - 12)},
		// a lone outlier holding the strict max reset (+4s), low wk -- the current bug picks ONLY this
		{"t": int64(15400), "wk": 11.0, "wk_reset": float64(base + 4)},
	}

	out := Envelope(entries, "wk", "wk_reset")

	// All five current-window rows must survive; the prior week's row must not.
	if len(out) != 5 {
		t.Fatalf("expected 5 current-window rows (real window + jitter + outlier), got %d", len(out))
	}
	// Every emitted row is normalized to the canonical (max) reset so downstream currentRun sees one window.
	for i, e := range out {
		if got := tof(e["wk_reset"]); got != float64(base+4) {
			t.Errorf("row %d: wk_reset = %v, want normalized max %d", i, got, base+4)
		}
	}
	// The whole point: a projection now fires instead of collapsing to a single sample.
	if _, ok := Project(out); !ok {
		t.Fatalf("Project returned no projection: envelope collapsed and burn/runway would be dead")
	}
}
