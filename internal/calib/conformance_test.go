package calib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/golden"
	"github.com/SeanLF/ccpool/internal/store"
)

// The $/1% calibration must match the committed goldens (conformance/golden/, Go-defined) for the
// compute (wk_runs run-detection, the Anthropic block filter, prorated cost_between, the pooled
// Δ-weighting) at displayed precision. We spawn a fake ccusage (CCPOOL_CCUSAGE_CMD -> fake-ccusage.sh,
// CCUSAGE_FIXTURE -> blocks JSON) so the compute is deterministic, force a recompute, and diff the
// resulting dpp to 4 decimals.

type computeFixture struct {
	Name   string      `json:"name"`
	Now    json.Number `json:"now"`
	Hist   string      `json:"hist"`
	Blocks string      `json:"blocks"`
}

func TestComputeConformance(t *testing.T) {
	root := repoRoot(t)
	fakeCmd := "sh " + filepath.Join(root, "conformance", "fake-ccusage.sh")
	fixtures := loadComputeFixtures(t, filepath.Join(root, "conformance", "compute_fixtures.json"))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "ccpool.db")
			blocksFixture := filepath.Join(dir, "blocks.json")
			if fx.Hist != "" {
				if err := store.SeedHistoryJSONL(dbPath, fx.Hist); err != nil {
					t.Fatalf("seed history: %v", err)
				}
			}
			mustWrite(t, blocksFixture, fx.Blocks)

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}

			// The calibration + blocks caches live in the store now (kv), so the isolated CCPOOL_DB is
			// the only cache staging needed -- the forced recompute writes both kv rows into it.
			t.Setenv("CCPOOL_DB", dbPath)
			t.Setenv("CCPOOL_HOME", dir)
			t.Setenv("CCPOOL_CCUSAGE_CMD", fakeCmd)
			t.Setenv("CCUSAGE_FIXTURE", blocksFixture)

			s, st := store.Open()
			if st != store.StateOK || s == nil {
				t.Fatalf("open = %v", st)
			}
			defer s.Close()

			goOut := "nil"
			if dpp, ok := DollarPerPct(s, now, true); ok {
				goOut = fmt.Sprintf("%.4f", dpp)
			}

			golden.Assert(t, filepath.Join(root, "conformance", "golden", "calib", fx.Name+".txt"), []byte(goOut))
		})
	}
}

func loadComputeFixtures(t *testing.T, path string) []computeFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []computeFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
