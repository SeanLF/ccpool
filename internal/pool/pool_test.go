package pool

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

// LoadSnapshots reads from the store now (not per-session files). A seeded temp DB must round-trip
// through GetWindow/Weekly/FiveHour/DataAge to the same values the map-based reconcile always gave.
func TestLoadSnapshotsFromStore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", dbPath)

	const now = 1720000000
	payloads := [][]byte{
		[]byte(`{"session_id":"s1","captured_at":1719999990,"rate_limits":{"seven_day":{"used_percentage":50,"resets_at":1720300000},"five_hour":{"used_percentage":30,"resets_at":1720003600}}}`),
		[]byte(`{"session_id":"s2","captured_at":1719999995,"rate_limits":{"seven_day":{"used_percentage":62,"resets_at":1720300000},"five_hour":{"used_percentage":20,"resets_at":1720003600}}}`),
	}
	if err := store.SeedSnapshots(dbPath, payloads); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	defer s.Close()

	snaps := LoadSnapshots(s)
	if len(snaps) != 2 {
		t.Fatalf("LoadSnapshots = %d rows, want 2", len(snaps))
	}
	wk, ok := Weekly(snaps, now)
	if !ok || wk.Used != 62 { // max used% across the two sessions on the shared reset
		t.Errorf("Weekly = %+v ok=%v, want Used 62", wk, ok)
	}
	fh, ok := FiveHour(snaps, now)
	if !ok || fh.Used != 30 {
		t.Errorf("FiveHour = %+v ok=%v, want Used 30", fh, ok)
	}
	age, ok := DataAge(snaps, now)
	if !ok || age != now-1719999995 { // freshest captured_at across sessions
		t.Errorf("DataAge = %d ok=%v, want %d", age, ok, now-1719999995)
	}
}

// A store that never got a snapshot (fresh install / warm-up) yields no snapshots, and a nil store
// (open failed) fails open to the same empty result.
func TestLoadSnapshotsEmptyStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	defer s.Close()
	if snaps := LoadSnapshots(s); len(snaps) != 0 {
		t.Fatalf("LoadSnapshots on empty store = %d, want 0", len(snaps))
	}
	if snaps := LoadSnapshots(nil); len(snaps) != 0 {
		t.Fatalf("LoadSnapshots(nil) = %d, want 0 (fail open)", len(snaps))
	}
}

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
