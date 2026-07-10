package calib

import (
	"os"
	"path/filepath"
	"testing"
)

// blocksArray accepts only the documented shapes (a bare array, or doc["blocks"]). A ccusage schema
// rename must NOT be silently matched to some other array field: it has to return nil so the caller
// fails LOUD ("unexpected shape; $ readout disabled") rather than misreading an unrelated array as
// cost blocks.
func TestBlocksArrayRejectsRenamedField(t *testing.T) {
	one := []any{map[string]any{"costUSD": 1}}
	if got := blocksArray(one); len(got) != 1 {
		t.Errorf("bare array should be the blocks list, got %v", got)
	}
	if got := blocksArray(map[string]any{"blocks": one}); len(got) != 1 {
		t.Errorf(`doc["blocks"] should be the blocks list, got %v`, got)
	}
	// Empty-but-present blocks must stay a non-nil slice ("ran, no cost yet"), NOT nil -- else the
	// caller would fail loud on a perfectly valid response.
	if got := blocksArray(map[string]any{"blocks": []any{}}); got == nil {
		t.Error(`empty {"blocks":[]} must return a non-nil slice, not nil`)
	}
	renamed := map[string]any{"windows": one, "meta": []any{"a"}} // no "blocks" key
	if got := blocksArray(renamed); got != nil {
		t.Errorf("renamed schema must return nil (fail loud), got %v", got)
	}
}

// These exercise the cache-read paths that the compute conformance test (which always forces a
// recompute) skips — the exact paths a float64-vs-json.Number decode bug silently disabled.

func TestCacheFreshPaths(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "calib.json")
	t.Setenv("CCPOOL_CALIB_CACHE", cache)

	const now = 1_000_000
	write := func(content string) {
		if err := os.WriteFile(cache, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Fresh cache (at == now, well within the 6h TTL).
	write(`{"dpp":2.5,"at":1000000}`)
	if Stale(now) {
		t.Error("Stale(now) = true for a fresh cache, want false")
	}
	if dpp, ok := DPP(); !ok || dpp != 2.5 {
		t.Errorf("DPP() = (%v, %v), want (2.5, true)", dpp, ok)
	}
	// force=false must hit the cache and return without recomputing (no history / ccusage present).
	if dpp, ok := DollarPerPct(now, false); !ok || dpp != 2.5 {
		t.Errorf("DollarPerPct(now,false) = (%v, %v), want (2.5, true)", dpp, ok)
	}

	// Integer dpp (a Go-written cache drops the .0) must still read back as numeric.
	write(`{"dpp":3,"at":1000000}`)
	if dpp, ok := DPP(); !ok || dpp != 3 {
		t.Errorf("DPP() with integer dpp = (%v, %v), want (3, true)", dpp, ok)
	}

	// Stale cache (older than the 6h TTL).
	write(`{"dpp":2.5,"at":900000}`)
	if !Stale(now) {
		t.Error("Stale(now) = false for a cache older than TTL, want true")
	}

	// Missing cache.
	os.Remove(cache)
	if !Stale(now) {
		t.Error("Stale(now) = false for a missing cache, want true")
	}
	if _, ok := DPP(); ok {
		t.Error("DPP() ok = true for a missing cache, want false")
	}
}
