package pool

import (
	"encoding/json"
	"testing"
)

// Snapshots from different sessions freeze resets_at at different render moments, so the SAME window's
// reset wobbles a few seconds between them (the A0 jitter). GetWindow must bucket resets within the
// tolerance of the max and take the max used% across the bucket; a strict max otherwise lands on a
// lone outlier snapshot and under-reports usage (the higher-used% snapshot with a 4s-earlier reset is
// silently dropped).
func TestGetWindowBucketsJitteredReset(t *testing.T) {
	const now, reset = 1720000000, 1720300000
	snaps := []map[string]any{
		// freshest usage, but its reset sits 4s BELOW the outlier's
		{"rate_limits": map[string]any{"seven_day": map[string]any{"used_percentage": json.Number("62"), "resets_at": json.Number("1720300000")}}},
		// a lone snapshot holding the strict-max reset with STALE (lower) usage
		{"rate_limits": map[string]any{"seven_day": map[string]any{"used_percentage": json.Number("41"), "resets_at": json.Number("1720300004")}}},
	}

	w, ok := GetWindow(snaps, "seven_day", now, Week+86400)
	if !ok {
		t.Fatal("expected a live window")
	}
	if w.Used != 62 {
		t.Errorf("Used = %v, want 62 (max across the jitter bucket, not the outlier's 41)", w.Used)
	}
	if w.Reset != reset+4 {
		t.Errorf("Reset = %d, want %d (newest reset)", w.Reset, reset+4)
	}
}
