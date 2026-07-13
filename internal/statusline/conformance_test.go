package statusline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SeanLF/ccpool/internal/golden"
	"github.com/SeanLF/ccpool/internal/store"
)

// The statusline render must stay byte-identical to the committed goldens (conformance/golden/,
// Go-defined; ANSI included). For every fixture we render in Go and diff against its golden; a diff
// is a regression until an intentional, reviewed change is refreshed via CCPOOL_UPDATE_GOLDEN=1.

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
	"CCPOOL_PACE_WEIGHTS", "CCPOOL_PACE_HOUR_WEIGHTS", "USAGE_TIER", "CCPOOL_CONFIG",
}

func TestStatuslineConformance(t *testing.T) {
	// Pin the zone: Go's .Local() must match the zone the goldens were captured under (UTC) so
	// scheduled-profile pace math lines up. Fixed here rather than relying on the launch environment.
	time.Local = time.UTC
	t.Setenv("TZ", "UTC")

	root := repoRoot(t)

	fixtures := loadFixtures(t, filepath.Join(root, "conformance", "fixtures.json"))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			applyEnv(t, fx.Env)
			// The calibration cache lives in the store (kv) now, so each case gets a fresh isolated DB
			// and seeds the $/1% into it (a case with no dpp leaves the row absent -> DPP fails open).
			dir := t.TempDir()
			t.Setenv("CCPOOL_HOME", dir)
			t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
			s, st := store.Open()
			if st != store.StateOK || s == nil {
				t.Fatalf("open = %v", st)
			}
			defer s.Close()
			seedCalib(t, s, fx)

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now %q: %v", fx.Now, err)
			}

			goRender := Render(s, fx.Payload, now)
			goCompact := RenderCompact(s, fx.Payload, now)

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

// applyEnv clears every fixture-controlled key, then sets the ones this fixture specifies, then
// isolates CCPOOL_CONFIG at a nonexistent path so config.Load() never picks up the dev's real
// ~/.ccpool/ccpool.json. t.Setenv restores the prior value at test end. TZ is left pinned to UTC by
// the parent test.
func applyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	t.Setenv("CCPOOL_CONFIG", filepath.Join(t.TempDir(), "no-config.json")) // isolate: never read the dev's real ~/.ccpool/ccpool.json
}

// seedCalib puts the fixture's $/1% into the kv calibration row (same {dpp,at} blob the file held).
// A case with no dpp leaves the row absent, so DPP() reads a cold cache and fails open.
func seedCalib(t *testing.T, s *store.Store, fx fixture) {
	t.Helper()
	if fx.CalibDPP == nil {
		return
	}
	blob := `{"dpp":` + fx.CalibDPP.String() + `,"at":` + fx.Now.String() + `}`
	if err := s.PutKV("calibration", []byte(blob)); err != nil {
		t.Fatalf("seed calib: %v", err)
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
