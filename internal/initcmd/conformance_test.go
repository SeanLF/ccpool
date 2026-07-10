package initcmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The Ruby Init module is the conformance oracle: for each fixture we run the Go Run and the Ruby
// Init.run against the SAME staged settings path (so the absolute paths in stdout match), then diff
// the captured stdout, the exit status, whether the path is still a symlink, and the resulting
// settings.json bytes. Byte-identical output AND resulting file is the bar (docs/GO-MIGRATION.md).

type initFixture struct {
	Name     string            `json:"name"`
	Now      json.Number       `json:"now"`
	Argv     []string          `json:"argv"`
	Env      map[string]string `json:"env"`
	Kind     string            `json:"kind"` // fresh | regular | symlink
	Settings *string           `json:"settings"`
	Dangling bool              `json:"dangling"`
}

// initEnvKeys are cleared before each case so fixtures don't leak env into one another.
var initEnvKeys = []string{"CCPOOL_CCUSAGE_CMD", "CCPOOL_SETTINGS", "USAGE_CACHE"}

func TestInitConformance(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	// Pin the zone for both sides (the preview's fmt_dur/pace math is local-zone sensitive).
	time.Local = time.UTC
	t.Setenv("TZ", "UTC")

	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "init_oracle.rb")

	// Pin the launcher to the Ruby reference path so the wired command strings match. Ruby derives
	// this from init.rb's __dir__ (= repo root); Go defaults to the running binary, so override it.
	prev := launcherOverride
	launcherOverride = filepath.Join(root, "bin", "ccpool")
	t.Cleanup(func() { launcherOverride = prev })

	for _, fx := range loadInitFixtures(t, filepath.Join(root, "conformance", "init_fixtures.json")) {
		t.Run(fx.Name, func(t *testing.T) {
			for _, k := range initEnvKeys {
				os.Unsetenv(k)
			}
			for k, v := range fx.Env {
				t.Setenv(k, v)
			}
			// Point the snapshot glob at an empty dir so the statusline preview finds nothing and
			// prints only its header on both sides (the preview render itself isn't under test here).
			t.Setenv("USAGE_CACHE", filepath.Join(t.TempDir(), "usage-cache.json"))

			dir := t.TempDir()
			settingsPath := filepath.Join(dir, "settings.json")
			t.Setenv("CCPOOL_SETTINGS", settingsPath)

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}

			// --- Go side ---
			stageFixture(t, fx, dir, settingsPath)
			goOut, goCode := captureStdout(func() error { return Run(fx.Argv, now) })
			goSym, goExists, goBody := inspect(settingsPath)
			cleanDir(t, dir)

			// --- Ruby oracle side (same path so stdout's absolute paths line up) ---
			stageFixture(t, fx, dir, settingsPath)
			rubyOut, rubyCode, rubySym, rubyExists, rubyBody := runInitOracle(t, oracle, fx, now)

			if goOut != rubyOut {
				t.Errorf("stdout mismatch\n go:   %q\n ruby: %q", goOut, rubyOut)
			}
			if goCode != rubyCode {
				t.Errorf("exit code mismatch: go=%d ruby=%d", goCode, rubyCode)
			}
			if goSym != rubySym {
				t.Errorf("symlink state mismatch: go=%v ruby=%v", goSym, rubySym)
			}
			if goExists != rubyExists {
				t.Errorf("exists mismatch: go=%v ruby=%v", goExists, rubyExists)
			}
			if !bytes.Equal(goBody, rubyBody) {
				t.Errorf("settings.json mismatch\n go:   %q\n ruby: %q", goBody, rubyBody)
			}
		})
	}
}

// stageFixture sets up the initial filesystem state a fixture describes.
func stageFixture(t *testing.T, fx initFixture, dir, settingsPath string) {
	t.Helper()
	switch fx.Kind {
	case "fresh":
		// no file
	case "regular":
		if err := os.WriteFile(settingsPath, []byte(deref(fx.Settings)), 0o644); err != nil {
			t.Fatalf("stage regular: %v", err)
		}
	case "symlink":
		target := filepath.Join(dir, "target.json")
		if fx.Dangling {
			target = filepath.Join(dir, "no-such-target.json") // deliberately not created
		} else if err := os.WriteFile(target, []byte(deref(fx.Settings)), 0o644); err != nil {
			t.Fatalf("stage symlink target: %v", err)
		}
		if err := os.Symlink(target, settingsPath); err != nil {
			t.Fatalf("stage symlink: %v", err)
		}
	default:
		t.Fatalf("unknown fixture kind %q", fx.Kind)
	}
}

func cleanDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			t.Fatalf("clean dir: %v", err)
		}
	}
}

// inspect reports the settings path's symlink/exists state and its bytes (via the path, following a
// symlink), matching what the Ruby oracle reports.
func inspect(path string) (isSymlink, exists bool, body []byte) {
	if fi, err := os.Lstat(path); err == nil {
		isSymlink = fi.Mode()&os.ModeSymlink != 0
	}
	if _, err := os.Stat(path); err == nil {
		exists = true
		body, _ = os.ReadFile(path)
	}
	return isSymlink, exists, body
}

// captureStdout redirects os.Stdout for the duration of fn and returns what it wrote plus a Ruby-
// style exit code (1 when fn returns an error, since Init.run's abort paths exit 1).
func captureStdout(fn func() error) (string, int) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	err := fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	_ = r.Close()
	code := 0
	if err != nil {
		code = 1
	}
	return buf.String(), code
}

func runInitOracle(t *testing.T, oracle string, fx initFixture, now int64) (out string, code int, isSymlink, exists bool, body []byte) {
	t.Helper()
	in, err := json.Marshal(map[string]any{"argv": fx.Argv, "now": now})
	if err != nil {
		t.Fatalf("marshal oracle input: %v", err)
	}
	cmd := exec.Command("ruby", oracle)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = os.Environ() // carries CCPOOL_SETTINGS, USAGE_CACHE, CCPOOL_CCUSAGE_CMD, TZ
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oracle failed: %v\nstderr: %s", err, stderr.String())
	}
	parts := bytes.SplitN(stdout.Bytes(), []byte{0}, 5)
	if len(parts) != 5 {
		t.Fatalf("oracle output missing NUL fields: %q", stdout.String())
	}
	code, err = atoi(parts[1])
	if err != nil {
		t.Fatalf("bad oracle exit code %q: %v", parts[1], err)
	}
	return string(parts[0]), code, string(parts[2]) == "1", string(parts[3]) == "1", parts[4]
}

func atoi(b []byte) (int, error) {
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return 0, err
	}
	return n, nil
}

func loadInitFixtures(t *testing.T, path string) []initFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []initFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- prune_history conformance ---

type pruneFixture struct {
	Name     string      `json:"name"`
	Now      json.Number `json:"now"`
	KeepDays string      `json:"keep_days"`
	Hist     string      `json:"hist"`
}

func TestPruneHistoryConformance(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "init_prune_oracle.rb")

	for _, fx := range loadPruneFixtures(t, filepath.Join(root, "conformance", "init_prune_fixtures.json")) {
		t.Run(fx.Name, func(t *testing.T) {
			os.Unsetenv("CCPOOL_HISTORY_KEEP_DAYS")
			t.Setenv("CCPOOL_HISTORY_KEEP_DAYS", fx.KeepDays)

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}
			keepDays, err := fx.parseKeepDays()
			if err != nil {
				t.Fatalf("bad keep_days: %v", err)
			}

			// Go side.
			goHist := filepath.Join(t.TempDir(), "hist.jsonl")
			if err := os.WriteFile(goHist, []byte(fx.Hist), 0o644); err != nil {
				t.Fatalf("write go hist: %v", err)
			}
			t.Setenv("CCPOOL_HISTORY", goHist)
			goRemoved, err := PruneHistory(now, keepDays)
			if err != nil {
				t.Fatalf("PruneHistory: %v", err)
			}
			goBody, err := os.ReadFile(goHist)
			if err != nil {
				t.Fatalf("read go hist: %v", err)
			}

			// Ruby oracle side.
			rubyRemoved, rubyBody := runPruneOracle(t, oracle, fx, filepath.Join(t.TempDir(), "hist.jsonl"))

			if goRemoved != rubyRemoved {
				t.Errorf("removed count mismatch: go=%d ruby=%d", goRemoved, rubyRemoved)
			}
			if !bytes.Equal(goBody, rubyBody) {
				t.Errorf("history mismatch\n go:   %q\n ruby: %q", goBody, rubyBody)
			}
		})
	}
}

func (fx pruneFixture) parseKeepDays() (float64, error) {
	var f float64
	if err := json.Unmarshal([]byte(fx.KeepDays), &f); err != nil {
		return 0, err
	}
	return f, nil
}

func runPruneOracle(t *testing.T, oracle string, fx pruneFixture, histPath string) (int, []byte) {
	t.Helper()
	in, err := json.Marshal(map[string]any{"now": fx.Now, "hist": fx.Hist})
	if err != nil {
		t.Fatalf("marshal oracle input: %v", err)
	}
	cmd := exec.Command("ruby", oracle)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = append(envWithout(os.Environ(), "CCPOOL_HISTORY"), "CCPOOL_HISTORY="+histPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oracle failed: %v\nstderr: %s", err, stderr.String())
	}
	parts := bytes.SplitN(stdout.Bytes(), []byte{0}, 2)
	if len(parts) != 2 {
		t.Fatalf("oracle output missing NUL separator: %q", stdout.String())
	}
	removed, err := atoi(parts[0])
	if err != nil {
		t.Fatalf("bad removed count %q: %v", parts[0], err)
	}
	return removed, parts[1]
}

func loadPruneFixtures(t *testing.T, path string) []pruneFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []pruneFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

// --- shared helpers (mirrors of the other conformance tests) ---

func envWithout(env []string, key string) []string {
	out := env[:0:0]
	prefix := key + "="
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			continue
		}
		out = append(out, e)
	}
	return out
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
