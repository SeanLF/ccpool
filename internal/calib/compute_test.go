package calib

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

func cacheStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// CachedCumulativeCost sums the cached Anthropic blocks WITHOUT spawning ccusage (it runs on the write
// hot path): non-gap Anthropic blocks only, gap/non-Anthropic excluded.
func TestCachedCumulativeCostSumsCachedBlocks(t *testing.T) {
	s := cacheStore(t)
	raw := `{"blocks":[` +
		`{"startTime":"2026-07-01T00:00:00Z","endTime":"2026-07-01T05:00:00Z","costUSD":10,"models":["claude-opus-4-8"],"isGap":false},` +
		`{"startTime":"2026-07-01T05:00:00Z","endTime":"2026-07-01T10:00:00Z","costUSD":20,"models":["claude-sonnet-5"],"isGap":false},` +
		`{"startTime":"2026-07-01T10:00:00Z","endTime":"2026-07-01T15:00:00Z","costUSD":99,"isGap":true},` +
		`{"startTime":"2026-07-01T15:00:00Z","endTime":"2026-07-01T20:00:00Z","costUSD":77,"models":["gpt-5.5"],"isGap":false}]}`
	blob, err := json.Marshal(map[string]any{"raw": raw, "at": 1}) // "at":1 = ancient, but cache-only ignores TTL
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutKV(blocksKey, blob); err != nil {
		t.Fatal(err)
	}
	got, ok := CachedCumulativeCost(s)
	if !ok || got != 30 { // 10 + 20; gap (99) and non-Anthropic gpt (77) excluded
		t.Fatalf("cumulative = %v ok=%v, want 30 true", got, ok)
	}
}

func TestCachedCumulativeCostColdCache(t *testing.T) {
	s := cacheStore(t)
	if _, ok := CachedCumulativeCost(s); ok {
		t.Fatal("cold cache should return ok=false, never spawning ccusage")
	}
}

func wp(bnd, minute int64, wk float64) store.WkPoint {
	return store.WkPoint{Bnd: bnd, Minute: minute, Wk: wk}
}

// Monotonic running-max within a window: stale-low concurrent reads that dip below the running value
// are noise; dw is (max - first), counted once, not re-counted across the spurious dips the old
// hard-fall split produced.
func TestRunsFromPointsMonotonicIgnoresDips(t *testing.T) {
	B := int64(1_000_000_000) // reset far ahead of the minutes -> nothing clipped
	pts := []store.WkPoint{
		wp(B, 100, 10),
		wp(B, 100+1800, 20),
		wp(B, 100+3000, 15), // stale-low dip
		wp(B, 100+4000, 30),
		wp(B, 100+5000, 25), // stale-low dip
		wp(B, 100+7200, 40),
	}
	runs := runsFromPoints(pts)
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d: %+v", len(runs), runs)
	}
	if runs[0].dw != 30 {
		t.Fatalf("dw = %v, want 30 (40-10, dips ignored)", runs[0].dw)
	}
	if runs[0].t0 != 100 || runs[0].t1 != 100+7200 {
		t.Fatalf("window = [%d,%d], want [100,%d]", runs[0].t0, runs[0].t1, 100+7200)
	}
}

// Clip at the reset epoch: samples still carrying a wk_reset after it fired are stale and excluded.
func TestRunsFromPointsClipsPostReset(t *testing.T) {
	B := int64(5000)
	pts := []store.WkPoint{
		wp(B, 100, 10),
		wp(B, 3700, 50),
		wp(B, 6000, 90), // minute > reset epoch -> clipped
	}
	runs := runsFromPoints(pts)
	if len(runs) != 1 {
		t.Fatalf("want 1 run, got %d", len(runs))
	}
	if runs[0].dw != 40 {
		t.Fatalf("dw = %v, want 40 (50-10; post-reset 90 clipped)", runs[0].dw)
	}
	if runs[0].t1 != 3700 {
		t.Fatalf("t1 = %d, want 3700 (clipped at reset)", runs[0].t1)
	}
}

// Guard: a sustained decrease within a window (a rolling-window / regime signal, not a brief dip)
// excludes the window rather than letting monotonic-max silently over-count.
func TestRunsFromPointsGuardExcludesSustainedDecrease(t *testing.T) {
	B := int64(1_000_000_000)
	pts := []store.WkPoint{
		wp(B, 100, 10),
		wp(B, 2000, 50),
		wp(B, 4000, 40), // drops >2 below running max 50; dip begins
		wp(B, 6000, 38),
		wp(B, 8000, 35), // still down 4000s later (> 1h) -> sustained
	}
	if runs := runsFromPoints(pts); len(runs) != 0 {
		t.Fatalf("sustained decrease should exclude the window, got %+v", runs)
	}
}

// Recency: only the last K complete windows feed calibration, so a regime change is tracked instead of
// blended across all history.
func TestRunsFromPointsRecencyKeepsLastK(t *testing.T) {
	var pts []store.WkPoint
	for b := int64(1); b <= 5; b++ {
		bnd := b * 1_000_000_000
		pts = append(pts, wp(bnd, bnd-7200, 10), wp(bnd, bnd-100, 40)) // dw=30, dt=7100
	}
	runs := runsFromPoints(pts)
	if len(runs) != 3 {
		t.Fatalf("recency: want last 3 windows, got %d", len(runs))
	}
	if runs[0].t0 != 3*1_000_000_000-7200 {
		t.Fatalf("kept the wrong windows; first run t0=%d", runs[0].t0)
	}
}

// combineWindows pools $/1% dw-weighted when the recent windows agree.
func TestCombineWindowsPoolsWhenAgree(t *testing.T) {
	got, ok := combineWindows([]windowCost{{cost: 250, dw: 10}, {cost: 290, dw: 10}})
	if !ok || got != 27 { // (250+290)/(10+10)
		t.Fatalf("pooled = %v ok=%v, want 27 true", got, ok)
	}
}

// combineWindows trusts the most-recent window when the windows disagree sharply (a regime shift),
// instead of averaging an old regime with a new one.
func TestCombineWindowsTrustsRecentOnDisagreement(t *testing.T) {
	got, ok := combineWindows([]windowCost{{cost: 50, dw: 10}, {cost: 300, dw: 10}}) // $5 vs $30, ratio 6
	if !ok || got != 30 {
		t.Fatalf("disagreement -> recent = %v ok=%v, want 30 true", got, ok)
	}
}

// runsFromPoints runs on the fail-open calibration path over pre-aggregated points that (pre-sanitizer)
// could be adversarial: it must never panic, and dw is non-negative by construction (max - first).
func FuzzRunsFromPoints(f *testing.F) {
	f.Add(int64(1_000_000_000), int64(100), int64(3800), int64(7200), 10.0, 25.0, 40.0)
	f.Fuzz(func(t *testing.T, bnd, m1, m2, m3 int64, w1, w2, w3 float64) {
		pts := []store.WkPoint{
			{Bnd: bnd, Minute: m1, Wk: w1},
			{Bnd: bnd, Minute: m2, Wk: w2},
			{Bnd: bnd, Minute: m3, Wk: w3},
		}
		for _, r := range runsFromPoints(pts) {
			if r.dw < 0 {
				t.Fatalf("negative dw %v from %+v", r.dw, pts)
			}
		}
	})
}
