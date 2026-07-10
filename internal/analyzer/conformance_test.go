package analyzer

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The `ccpool review` readout must be byte-identical to Ruby CCPool.review (docs/GO-MIGRATION.md).
// For each fixture we stage the same transcript files (in a Go temp dir and, independently, the
// oracle's own temp dir), render in Go, and diff against the Ruby oracle's stdout. Ruby is the
// source of truth; a diff is a Go bug until proven otherwise.

type fileSpec struct {
	Lines []json.RawMessage `json:"lines"`
	Mtime *json.Number      `json:"mtime"`
}

type reviewFixture struct {
	Name  string              `json:"name"`
	Now   json.Number         `json:"now"`
	Args  []string            `json:"args"`
	Env   map[string]string   `json:"env"`
	Files map[string]fileSpec `json:"files"`
}

// envKeys are every variable a fixture may set/clear; wiped before each case so cases don't leak env
// and an absent key is truly absent (Ruby treats "" as present-and-truthy, which would misread).
var envKeys = []string{"CCPOOL_PROJECTS", "CCPOOL_LOW_OUTPUT"}

func TestReviewConformance(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	// Pin the zone for both sides so RFC3339 timestamp -> unix and the window cutoff agree.
	time.Local = time.UTC
	t.Setenv("TZ", "UTC")

	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "review_oracle.rb")
	fixtures := loadReviewFixtures(t, filepath.Join(root, "conformance", "review_fixtures.json"))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			for _, k := range envKeys {
				t.Setenv(k, "")
				os.Unsetenv(k)
			}
			for k, v := range fx.Env {
				t.Setenv(k, v)
			}
			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now %q: %v", fx.Now, err)
			}

			// Go side: stage into its own projects dir.
			goProjects := t.TempDir()
			t.Setenv("CCPOOL_PROJECTS", goProjects)
			stageFiles(t, goProjects, fx, now)
			goOut := RenderCommand(fx.Args, now)

			// Ruby side: the oracle stages into a separate projects dir it reads from CCPOOL_PROJECTS.
			rubyProjects := t.TempDir()
			rubyOut := runReviewOracle(t, oracle, fx, rubyProjects)

			if goOut != string(rubyOut) {
				t.Errorf("review mismatch\n go:   %q\n ruby: %q", goOut, string(rubyOut))
			}
		})
	}
}

// stageFiles writes each fixture transcript file and stamps its mtime (fixture override or `now`).
func stageFiles(t *testing.T, base string, fx reviewFixture, now int64) {
	t.Helper()
	for rel, spec := range fx.Files {
		path := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		var b bytes.Buffer
		for _, line := range spec.Lines {
			b.Write(line)
			b.WriteByte('\n')
		}
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		mt := now
		if spec.Mtime != nil {
			if v, err := spec.Mtime.Int64(); err == nil {
				mt = v
			}
		}
		tm := time.Unix(mt, 0)
		if err := os.Chtimes(path, tm, tm); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}
}

func loadReviewFixtures(t *testing.T, path string) []reviewFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []reviewFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

// runReviewOracle runs CCPool.review through the Ruby oracle with CCPOOL_PROJECTS pointed at its own
// dir (frozen into a constant at require time, so it must be in the process env before load).
func runReviewOracle(t *testing.T, oracle string, fx reviewFixture, projects string) []byte {
	t.Helper()
	args := fx.Args
	if args == nil {
		args = []string{}
	}
	in, err := json.Marshal(map[string]any{"now": fx.Now, "args": args, "files": fx.Files})
	if err != nil {
		t.Fatalf("marshal oracle input: %v", err)
	}
	cmd := exec.Command("ruby", oracle)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = append(envWithout(os.Environ(), "CCPOOL_PROJECTS"), "CCPOOL_PROJECTS="+projects)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oracle failed: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.Bytes()
}

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
