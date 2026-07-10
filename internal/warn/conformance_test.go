package warn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// warn's emitted text and PostToolUse hook JSON must be byte-identical to Ruby Warn.run. For each
// fixture we stage the same snapshots + throttle markers on both sides and diff the output.

type warnFixture struct {
	Name    string                 `json:"name"`
	Now     json.Number            `json:"now"`
	Env     map[string]string      `json:"env"`
	Payload map[string]any         `json:"payload"`
	Markers map[string]json.Number `json:"markers"`
	Snaps   []map[string]any       `json:"snaps"`
}

var warnEnvKeys = []string{
	"CCPOOL_WARN_STALE_SECS", "CCPOOL_WARN_THROTTLE_SECS", "CCPOOL_WARN_CTX_PCT",
	"CCPOOL_WARN_CTX_LEFT", "CCPOOL_WARN_CTX_THROTTLE_SECS", "CCPOOL_WARN_5H_PCT",
	"CCPOOL_PACE_MARGIN", "CCPOOL_COAST_SECS", "CCPOOL_PACE_PROFILE", "CCPOOL_WORK_DAYS",
	"CCPOOL_WAKE_HOURS",
}

var markerKeyUnsafe = regexp.MustCompile(`[^\w.-]`)

func TestWarnConformance(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	time.Local = time.UTC // pin Go's zone to match the oracle's TZ=UTC (scheduled-profile pace)
	t.Setenv("TZ", "UTC")
	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "warn_oracle.rb")
	fixtures := loadWarnFixtures(t, filepath.Join(root, "conformance", "warn_fixtures.json"))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			for _, k := range warnEnvKeys {
				os.Unsetenv(k)
			}
			for k, v := range fx.Env {
				t.Setenv(k, v)
			}
			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}

			// Go side.
			goCache, goTmp := stage(t, fx)
			t.Setenv("USAGE_CACHE", filepath.Join(goCache, "usage-cache.json"))
			t.Setenv("TMPDIR", goTmp)
			goOut := Run(fx.Payload, now)

			// Ruby side (its own dirs).
			rubyOut := runWarnOracle(t, oracle, fx, now)

			if goOut != rubyOut {
				t.Errorf("warn mismatch\n go:   %q\n ruby: %q", goOut, rubyOut)
			}
		})
	}
}

// stage writes the fixture's snapshots and pre-seeded markers into fresh temp dirs, returning the
// snapshot-cache dir and the TMPDIR. Used for the Go side; the Ruby side stages its own via env.
func stage(t *testing.T, fx warnFixture) (cacheDir, tmpDir string) {
	t.Helper()
	cacheDir = t.TempDir()
	tmpDir = t.TempDir()
	writeSnaps(t, cacheDir, fx.Snaps)
	writeMarkers(t, tmpDir, fx)
	return cacheDir, tmpDir
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

func writeMarkers(t *testing.T, tmpDir string, fx warnFixture) {
	t.Helper()
	scope := "global"
	if sid, ok := fx.Payload["session_id"].(string); ok && sid != "" {
		scope = markerKeyUnsafe.ReplaceAllString(sid, "")
	}
	for sigKey, ts := range fx.Markers {
		name := filepath.Join(tmpDir, "claude-"+sigKey+"-"+scope)
		if err := os.WriteFile(name, []byte(ts.String()), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
}

func loadWarnFixtures(t *testing.T, path string) []warnFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []warnFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

func runWarnOracle(t *testing.T, oracle string, fx warnFixture, now int64) string {
	t.Helper()
	cacheDir := t.TempDir()
	tmpDir := t.TempDir()
	writeSnaps(t, cacheDir, fx.Snaps)
	writeMarkers(t, tmpDir, fx)

	in, err := json.Marshal(map[string]any{"now": fx.Now, "payload": fx.Payload})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	env := append(
		os.Environ(),
		"USAGE_CACHE="+filepath.Join(cacheDir, "usage-cache.json"),
		"TMPDIR="+tmpDir,
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
