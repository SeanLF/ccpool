package history

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "seed_oracle.rb")
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

			// Ruby oracle side (its own history file).
			rubyHist := filepath.Join(t.TempDir(), "hist.jsonl")
			rubyOut := runSeedOracle(t, oracle, fx, rubyHist)

			if !bytes.Equal(goOut, rubyOut) {
				t.Errorf("history mismatch\n go:   %q\n ruby: %q", goOut, rubyOut)
			}
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

func runSeedOracle(t *testing.T, oracle string, fx seedFixture, histPath string) []byte {
	t.Helper()
	in, err := json.Marshal(map[string]any{"now": fx.Now, "payload": fx.Payload, "hist": fx.Hist})
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
