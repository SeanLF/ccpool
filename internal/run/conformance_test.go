package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// downshift_env's decision (env overrides + message) must be byte-identical to Ruby
// CCPool.downshift_env. `run` itself exec's (replaces the process image, hard to diff), so only this
// pure decision is conformance-checked: for each fixture we stage the same snapshots (+ optional
// calibration cache / fake ccusage for the estimated tier) on both sides and diff "<msg>\n<env-json>".

type dsFixture struct {
	Name   string            `json:"name"`
	Now    json.Number       `json:"now"`
	Env    map[string]string `json:"env"`
	Calib  string            `json:"calib"`  // calibration cache JSON ("" = none)
	Blocks string            `json:"blocks"` // ccusage blocks JSON fixture ("" = none)
	Snaps  []map[string]any  `json:"snaps"`
}

// Knobs read fresh by the Ruby constants at load; reset between fixtures so one doesn't leak forward.
var dsEnvKeys = []string{
	"CCPOOL_DOWNSHIFT", "CCPOOL_PACE_MARGIN", "CCPOOL_COAST_SECS", "CCPOOL_5H_CAP",
	"CCPOOL_DOWNSHIFT_MODEL", "CCPOOL_DOWNSHIFT_EFFORT", "CCPOOL_STALE_SECS", "CCPOOL_CALIB_TTL",
	"CCPOOL_PACE_PROFILE", "CCPOOL_WORK_DAYS", "CCPOOL_WAKE_HOURS", "USAGE_TIER",
}

func TestDownshiftConformance(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	time.Local = time.UTC // pin Go's zone to match the oracle's TZ=UTC (scheduled-profile pace)
	t.Setenv("TZ", "UTC")
	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "downshift_oracle.rb")
	fakeCmd := "sh " + filepath.Join(root, "conformance", "fake-ccusage.sh")
	fixtures := loadFixtures(t, filepath.Join(root, "conformance", "downshift_fixtures.json"))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			for _, k := range dsEnvKeys {
				os.Unsetenv(k)
			}
			for k, v := range fx.Env {
				t.Setenv(k, v)
			}
			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}

			// Shared, read-only inputs (snapshots, ccusage fixture, calib cache) both sides read.
			dir := t.TempDir()
			writeSnaps(t, dir, fx.Snaps)
			blocksFixture := filepath.Join(dir, "blocks.json")
			mustWrite(t, blocksFixture, fx.Blocks)
			calibPath := filepath.Join(dir, "calib.json")
			if fx.Calib != "" {
				mustWrite(t, calibPath, fx.Calib)
			}

			// Go side. History is absent (no path) so the calib compute never spawns anything;
			// the blocks cache is per-side so the two runs' ccusage caches don't collide.
			t.Setenv("USAGE_CACHE", filepath.Join(dir, "usage-cache.json"))
			t.Setenv("CCPOOL_CCUSAGE_CMD", fakeCmd)
			t.Setenv("CCUSAGE_FIXTURE", blocksFixture)
			t.Setenv("CCPOOL_CALIB_CACHE", calibPath)
			t.Setenv("CCPOOL_HISTORY", filepath.Join(dir, "history.jsonl"))
			t.Setenv("CCPOOL_BLOCKS_CACHE", filepath.Join(dir, "go-blocks.json"))
			env, msg := DownshiftEnv(now)
			goOut := formatDS(t, env, msg)

			rubyOut := runOracle(t, oracle, now, filepath.Join(dir, "ruby-blocks.json"))

			if goOut != rubyOut {
				t.Errorf("downshift mismatch\n go:   %q\n ruby: %q", goOut, rubyOut)
			}
		})
	}
}

// formatDS renders the diffable pair: the message, then the env as key-sorted JSON. Go's json.Marshal
// sorts map keys; Ruby's oracle does env.sort.to_h -> the same order.
func formatDS(t *testing.T, env map[string]string, msg string) string {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	return msg + "\n" + string(b)
}

func writeSnaps(t *testing.T, dir string, snaps []map[string]any) {
	t.Helper()
	for i, s := range snaps {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal snap: %v", err)
		}
		name := filepath.Join(dir, fmt.Sprintf("usage-cache-snap%d.json", i))
		if err := os.WriteFile(name, b, 0o644); err != nil {
			t.Fatalf("write snap: %v", err)
		}
	}
}

func loadFixtures(t *testing.T, path string) []dsFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []dsFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

// runOracle runs the Ruby oracle, inheriting the staged env (USAGE_CACHE, CCPOOL_CALIB_CACHE, the
// fake ccusage, the knobs) but with its own blocks cache so it never reads the Go side's.
func runOracle(t *testing.T, oracle string, now int64, rubyBlocksCache string) string {
	t.Helper()
	in, err := json.Marshal(map[string]any{"now": now})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	env := append(os.Environ(), "CCPOOL_BLOCKS_CACHE="+rubyBlocksCache)
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
