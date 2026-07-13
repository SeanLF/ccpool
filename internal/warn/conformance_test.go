package warn

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/SeanLF/ccpool/internal/golden"
	"github.com/SeanLF/ccpool/internal/store"
)

// warn's emitted text and PostToolUse hook JSON must stay byte-identical to the committed goldens
// (conformance/golden/, Go-defined). For each fixture we stage the same snapshots + throttle markers
// and diff the output against its golden.

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
	time.Local = time.UTC // pin Go's zone to the zone the goldens were captured under (scheduled-profile pace)
	t.Setenv("TZ", "UTC")
	root := repoRoot(t)
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
			_, goTmp := stage(t, fx)
			t.Setenv("TMPDIR", goTmp)
			goOut := Run(fx.Payload, now)

			golden.Assert(t, filepath.Join(root, "conformance", "golden", "warn", fx.Name+".txt"), []byte(goOut))
		})
	}
}

// stage seeds the fixture's snapshots into an isolated store DB and its pre-seeded markers into a temp
// TMPDIR, returning the cache dir (also the CCPOOL_HOME) and the TMPDIR. warn reads snapshots from the
// store now, so this isolates CCPOOL_HOME/CCPOOL_DB rather than writing per-session cache files.
func stage(t *testing.T, fx warnFixture) (cacheDir, tmpDir string) {
	t.Helper()
	cacheDir = t.TempDir()
	tmpDir = t.TempDir()
	seedSnaps(t, cacheDir, fx.Snaps)
	writeMarkers(t, tmpDir, fx)
	return cacheDir, tmpDir
}

func seedSnaps(t *testing.T, dir string, snaps []map[string]any) {
	t.Helper()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_HOME", dir) // isolate every ~/.ccpool-derived path off the dev's real state
	t.Setenv("CCPOOL_DB", dbPath)
	if len(snaps) == 0 {
		return
	}
	var payloads [][]byte
	for _, s := range snaps {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal snap: %v", err)
		}
		payloads = append(payloads, b)
	}
	if err := store.SeedSnapshots(dbPath, payloads); err != nil {
		t.Fatalf("seed snapshots: %v", err)
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
