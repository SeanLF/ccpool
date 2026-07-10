package history

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/golden"
)

// The history log rows must be byte-identical to Ruby's seed_history output (key order, number
// literals, null fields) so Go and Ruby statuslines can interleave appends. For each fixture we
// seed a file in Go and diff it against the file the Ruby oracle produces from the same input.

type seedFixture struct {
	Name    string            `json:"name"`
	Now     json.Number       `json:"now"`
	Env     map[string]string `json:"env"`
	Hist    string            `json:"hist"`
	Payload map[string]any    `json:"payload"`
}

var seedEnvKeys = []string{"USAGE_TIER", "CCPOOL_HISTORY_MIN_INTERVAL"}

func TestSeedConformance(t *testing.T) {
	root := repoRoot(t)
	fixtures := loadSeedFixtures(t, filepath.Join(root, "conformance", "seed_fixtures.json"))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			for _, k := range seedEnvKeys {
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
			goHist := filepath.Join(t.TempDir(), "hist.jsonl")
			if err := os.WriteFile(goHist, []byte(fx.Hist), 0o644); err != nil {
				t.Fatalf("write go hist: %v", err)
			}
			t.Setenv("CCPOOL_HISTORY", goHist)
			if err := Seed(fx.Payload, now); err != nil {
				t.Fatalf("Seed: %v", err)
			}
			goOut, err := os.ReadFile(goHist)
			if err != nil {
				t.Fatalf("read go hist: %v", err)
			}

			golden.Assert(t, filepath.Join(root, "conformance", "golden", "history", fx.Name+".txt"), goOut)
		})
	}
}

func loadSeedFixtures(t *testing.T, path string) []seedFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []seedFixture
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
