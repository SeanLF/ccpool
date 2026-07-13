package analyzer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SeanLF/ccpool/internal/golden"
)

// The `ccpool review` readout must stay byte-identical to the committed goldens (conformance/golden/,
// Go-defined). For each fixture we stage the same transcript files in a Go temp dir, render in Go, and
// diff against its golden; a diff is a regression until an intentional, reviewed change is refreshed
// via CCPOOL_UPDATE_GOLDEN=1.

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
	// Pin the zone so RFC3339 timestamp -> unix and the window cutoff agree (goldens captured under UTC).
	time.Local = time.UTC
	t.Setenv("TZ", "UTC")

	root := repoRoot(t)
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

			golden.Assert(t, filepath.Join(root, "conformance", "golden", "analyzer", fx.Name+".txt"), []byte(goOut))
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
