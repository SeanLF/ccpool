package pool

import (
	"encoding/json"
	"strconv"
	"testing"

	"pgregory.net/rapid"
)

// GetWindow's contract is "newest plausible reset, max used% on it" -- the reconcile rule the whole
// account-global % depends on. Over clean snapshots the result must be exactly the max reset and the
// max used% among snapshots whose reset sits within the jitter tolerance of it. This is an oracle
// property: it re-derives the expected answer independently of the implementation.
func TestGetWindowReconcile(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := rapid.Int64Range(1_000_000_000, 2_000_000_000).Draw(t, "now")
		maxAhead := rapid.Int64Range(3600, Week+86400).Draw(t, "maxAhead")
		n := rapid.IntRange(1, 8).Draw(t, "n")

		useds := make([]float64, n)
		resets := make([]int64, n)
		snaps := make([]map[string]any, n)
		for i := 0; i < n; i++ {
			useds[i] = float64(rapid.IntRange(0, 100).Draw(t, "used")) // integral -> exact float equality
			resets[i] = now + rapid.Int64Range(1, maxAhead).Draw(t, "resetAhead")
			snaps[i] = map[string]any{"rate_limits": map[string]any{"seven_day": map[string]any{
				"used_percentage": json.Number(strconv.FormatFloat(useds[i], 'f', -1, 64)),
				"resets_at":       json.Number(strconv.FormatInt(resets[i], 10)),
			}}}
		}

		w, ok := GetWindow(snaps, "seven_day", now, maxAhead)
		if !ok {
			t.Fatal("expected a window from >=1 plausible snapshot")
		}

		var wantReset int64
		for _, r := range resets {
			wantReset = max(wantReset, r)
		}
		var wantUsed float64
		for i, r := range resets {
			if wantReset-r <= resetJitter {
				wantUsed = max(wantUsed, useds[i])
			}
		}
		if w.Reset != wantReset {
			t.Fatalf("Reset=%d want %d (newest)", w.Reset, wantReset)
		}
		if w.Used != wantUsed {
			t.Fatalf("Used=%v want %v (max within jitter of newest)", w.Used, wantUsed)
		}
	})
}
