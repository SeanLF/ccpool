package statusline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SeanLF/ccpool/internal/golden"
)

// The statusline render must be byte-identical to the Ruby statusline.rb (ANSI included), which is
// the conformance oracle (docs/GO-MIGRATION.md). For every fixture we render in Go and diff against
// the live Ruby output. Ruby is the source of truth; a diff is a Go bug until proven otherwise.

type fixture struct {
	Name     string            `json:"name"`
	Now      json.Number       `json:"now"`
	Env      map[string]string `json:"env"`
	CalibDPP *json.Number      `json:"calib_dpp"`
	Payload  map[string]any    `json:"payload"`
}

// envKeys are every variable a fixture may set; cleared before each case so cases don't leak env.
var envKeys = []string{
	"NO_COLOR", "TERM", "COLUMNS", "CCPOOL_BAR_COLOR", "CCPOOL_PACE_PROFILE",
	"CCPOOL_WORK_DAYS", "CCPOOL_WAKE_HOURS", "CCPOOL_PACE_FLOOR",
	"CCPOOL_PACE_WEIGHTS", "CCPOOL_PACE_HOUR_WEIGHTS", "USAGE_TIER",
}

func TestStatuslineConformance(t *testing.T) {
	// Pin the zone: Go's .Local() must match the zone the goldens were captured under (UTC) so
	// scheduled-profile pace math lines up. Fixed here rather than relying on the launch environment.
	time.Local = time.UTC
	t.Setenv("TZ", "UTC")

	root := repoRoot(t)

	fixtures := loadFixtures(t, filepath.Join(root, "conformance", "fixtures.json"))
	calibPath := filepath.Join(t.TempDir(), "calib.json")
	t.Setenv("CCPOOL_CALIB_CACHE", calibPath)

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			applyEnv(t, fx.Env)
			writeCalib(t, calibPath, fx)

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now %q: %v", fx.Now, err)
			}

			goRender := Render(fx.Payload, now)
			goCompact := RenderCompact(fx.Payload, now)

			golden.Assert(t, filepath.Join(root, "conformance", "golden", "statusline", fx.Name+".render.txt"), []byte(goRender))
			golden.Assert(t, filepath.Join(root, "conformance", "golden", "statusline", fx.Name+".compact.txt"), []byte(goCompact))
		})
	}
}

func loadFixtures(t *testing.T, path string) []fixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keep payload numbers as json.Number so int/float form survives (matches Ruby JSON.parse)
	var fs []fixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

// applyEnv clears every fixture-controlled key, then sets the ones this fixture specifies. t.Setenv
// restores the prior value at test end. TZ is left pinned to UTC by the parent test.
func applyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

// writeCalib writes (or removes) the calibration cache so both sides read the same $/1%.
func writeCalib(t *testing.T, path string, fx fixture) {
	t.Helper()
	if fx.CalibDPP == nil {
		os.Remove(path)
		return
	}
	content := `{"dpp":` + fx.CalibDPP.String() + `,"at":` + fx.Now.String() + `}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write calib: %v", err)
	}
}

// repoRoot walks up from the test's working directory to the module root (the dir with go.mod).
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
