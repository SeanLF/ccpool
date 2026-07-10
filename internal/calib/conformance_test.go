package calib

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The $/1% calibration must match Ruby's compute (wk_runs run-detection, the Anthropic block
// filter, prorated cost_between, the pooled Δ-weighting) at displayed precision. Both sides spawn
// the same fake ccusage (CCPOOL_CCUSAGE_CMD -> fake-ccusage.sh, CCUSAGE_FIXTURE -> blocks JSON) so
// the compute is deterministic; we force a recompute and diff the resulting dpp to 4 decimals.

type computeFixture struct {
	Name   string      `json:"name"`
	Now    json.Number `json:"now"`
	Hist   string      `json:"hist"`
	Blocks string      `json:"blocks"`
}

func TestComputeConformance(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "compute_oracle.rb")
	fakeCmd := "sh " + filepath.Join(root, "conformance", "fake-ccusage.sh")
	fixtures := loadComputeFixtures(t, filepath.Join(root, "conformance", "compute_fixtures.json"))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			dir := t.TempDir()
			histPath := filepath.Join(dir, "hist.jsonl")
			blocksFixture := filepath.Join(dir, "blocks.json")
			mustWrite(t, histPath, fx.Hist)
			mustWrite(t, blocksFixture, fx.Blocks)

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}

			// Shared env; distinct cache files per side so neither reads the other's.
			t.Setenv("CCPOOL_HISTORY", histPath)
			t.Setenv("CCPOOL_CCUSAGE_CMD", fakeCmd)
			t.Setenv("CCUSAGE_FIXTURE", blocksFixture)
			t.Setenv("CCPOOL_BLOCKS_CACHE", filepath.Join(dir, "go-blocks.json"))
			t.Setenv("CCPOOL_CALIB_CACHE", filepath.Join(dir, "go-calib.json"))

			goOut := "nil"
			if dpp, ok := DollarPerPct(now, true); ok {
				goOut = fmt.Sprintf("%.4f", dpp)
			}

			rubyOut := runComputeOracle(t, oracle, fx, dir)

			if goOut != rubyOut {
				t.Errorf("dpp mismatch: go=%s ruby=%s", goOut, rubyOut)
			}
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

func runComputeOracle(t *testing.T, oracle string, fx computeFixture, dir string) string {
	t.Helper()
	in, err := json.Marshal(map[string]any{"now": fx.Now})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Ruby uses its own cache files; everything else (history, fake ccusage, fixture) is shared.
	env := append(
		os.Environ(),
		"CCPOOL_BLOCKS_CACHE="+filepath.Join(dir, "ruby-blocks.json"),
		"CCPOOL_CALIB_CACHE="+filepath.Join(dir, "ruby-calib.json"),
	)
	cmd := exec.Command("ruby", oracle)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oracle failed: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String()
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
