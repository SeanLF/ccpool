package status

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SeanLF/ccpool/internal/golden"
)

// The status + check readouts must be byte-identical to the Ruby CCPool.status / Check.report, which
// are the conformance oracle (docs/GO-MIGRATION.md). For every fixture we stage the same snapshots +
// history + calibration/ccusage on both sides, run the Go readout, and diff it against the live Ruby
// output. Ruby is the source of truth; a diff is a Go bug until proven otherwise.

type readoutFixture struct {
	Name     string            `json:"name"`
	Now      json.Number       `json:"now"`
	Env      map[string]string `json:"env"`
	Snaps    []map[string]any  `json:"snaps"`
	RawSnaps []string          `json:"raw_snaps"` // verbatim snapshot file bodies (for corrupt cases)
	Hist     string            `json:"hist"`      // history JSONL body; empty -> no history file
	CalibDPP *json.Number      `json:"calib_dpp"` // seed the $/1% cache; nil -> no cache
	Blocks   string            `json:"blocks"`    // fake-ccusage blocks JSON (for CostSince)
}

// readoutEnvKeys are every CCPOOL_* knob a fixture may set; cleared before each case so cases don't
// leak env into one another.
var readoutEnvKeys = []string{
	"CCPOOL_CHECK_SES_FULL", "CCPOOL_CHECK_SES_SOON_SECS", "CCPOOL_CHECK_WEEKLY_LOW",
	"CCPOOL_CHECK_STALE_SECS", "CCPOOL_PACE_MARGIN", "CCPOOL_COAST_SECS", "CCPOOL_CHECK_IDLE_WARN_H",
	"CCPOOL_CHECK_BURNDOWN_FORFEIT", "CCPOOL_STALE_SECS", "CCPOOL_CLOCK", "CCPOOL_CACHE_KEEP_SECS",
	"CCPOOL_HISTORY_KEEP_DAYS", "CCPOOL_HISTORY_WARN_MB", "CCPOOL_CALIB_TTL", "CCPOOL_RUNWAY_FAST",
	"CCPOOL_RUNWAY_SLOW", "CCPOOL_RUNWAY_MIN_DENSITY", "CCPOOL_PACE_PROFILE", "CCPOOL_WORK_DAYS",
	"CCPOOL_WAKE_HOURS", "CCPOOL_PACE_FLOOR", "CCPOOL_PACE_WEIGHTS", "CCPOOL_PACE_HOUR_WEIGHTS",
	"CCPOOL_CONFIG",
}

func TestStatusConformance(t *testing.T) {
	runReadoutConformance(t, "status", "status_fixtures.json", func(now int64) string {
		return strings.Join(Status(now), "\n") + "\n"
	})
}

func TestCheckConformance(t *testing.T) {
	runReadoutConformance(t, "check", "check_fixtures.json", func(now int64) string {
		lines, code := Report(now)
		return strconv.Itoa(code) + "\n" + strings.Join(lines, "\n")
	})
}

// runReadoutConformance drives one fixture file: stage each case, render Go via goRender, and diff
// byte-for-byte against the committed golden (which names the output family: "status" or "check").
func runReadoutConformance(t *testing.T, which, fixturesFile string, goRender func(int64) string) {
	// Pin the zone: check's %Z line and the scheduled-profile pace depend on it (goldens captured under UTC).
	time.Local = time.UTC
	t.Setenv("TZ", "UTC")

	root := repoRoot(t)
	fakeCmd := "sh " + filepath.Join(root, "conformance", "fake-ccusage.sh")
	fixtures := loadReadoutFixtures(t, filepath.Join(root, "conformance", fixturesFile))

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			for _, k := range readoutEnvKeys {
				os.Unsetenv(k)
			}
			for k, v := range fx.Env {
				t.Setenv(k, v)
			}
			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now %q: %v", fx.Now, err)
			}

			stageReadout(t, fx, fakeCmd)
			goOut := goRender(now)

			golden.Assert(t, filepath.Join(root, "conformance", "golden", "status", fx.Name+"."+which+".txt"), []byte(goOut))
		})
	}
}

// stageReadout writes the shared inputs (snapshots, history, blocks fixture) into a temp dir and
// points the Go process env at them + Go-private cache files. It returns the env slice the Ruby
// oracle should run with: the shared inputs (inherited) plus Ruby-private cache files so the two
// processes never write over each other's calibration / blocks caches.
func stageReadout(t *testing.T, fx readoutFixture, fakeCmd string) []string {
	t.Helper()
	inputDir := t.TempDir()

	// Snapshots: structured objects + any verbatim raw bodies (corrupt cases).
	idx := 0
	for _, s := range fx.Snaps {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal snap: %v", err)
		}
		writeFile(t, filepath.Join(inputDir, fmt.Sprintf("usage-cache-snap%d.json", idx)), string(b))
		idx++
	}
	for _, raw := range fx.RawSnaps {
		writeFile(t, filepath.Join(inputDir, fmt.Sprintf("usage-cache-snap%d.json", idx)), raw)
		idx++
	}

	// History (absent when empty -> Burn.read returns []).
	histPath := filepath.Join(inputDir, "missing-history.jsonl")
	if fx.Hist != "" {
		histPath = filepath.Join(inputDir, "history.jsonl")
		writeFile(t, histPath, fx.Hist)
	}

	// Blocks fixture for the fake ccusage (always present so CCUSAGE_FIXTURE resolves; empty -> the
	// fake emits nothing -> ccusage "unavailable").
	blocksPath := filepath.Join(inputDir, "blocks.json")
	writeFile(t, blocksPath, fx.Blocks)

	goDir := t.TempDir()
	rubyDir := t.TempDir()

	// Shared inputs on the Go process env (inherited by the oracle via os.Environ).
	t.Setenv("USAGE_CACHE", filepath.Join(inputDir, "usage-cache.json"))
	t.Setenv("CCPOOL_HISTORY", histPath)
	t.Setenv("CCPOOL_CONFIG", filepath.Join(inputDir, "no-config.json")) // isolate: never read the dev's real ~/.claude/ccpool.json
	t.Setenv("CCPOOL_CCUSAGE_CMD", fakeCmd)
	t.Setenv("CCUSAGE_FIXTURE", blocksPath)
	t.Setenv("CCPOOL_BLOCKS_CACHE", filepath.Join(goDir, "blocks-cache.json"))

	goCalib := filepath.Join(goDir, "calib.json")
	rubyCalib := filepath.Join(rubyDir, "calib.json")
	if fx.CalibDPP != nil {
		body := `{"dpp":` + fx.CalibDPP.String() + `,"at":` + fx.Now.String() + `}`
		writeFile(t, goCalib, body)
		writeFile(t, rubyCalib, body)
	}
	t.Setenv("CCPOOL_CALIB_CACHE", goCalib) // missing file -> ReadCache nil -> no $

	// Ruby-private cache files; everything else (snapshots, history, ccusage) is shared/inherited.
	return append(
		os.Environ(),
		"CCPOOL_BLOCKS_CACHE="+filepath.Join(rubyDir, "blocks-cache.json"),
		"CCPOOL_CALIB_CACHE="+rubyCalib,
	)
}

func loadReadoutFixtures(t *testing.T, path string) []readoutFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keep snapshot/history numbers as json.Number (matches Ruby JSON.parse int/float)
	var fs []readoutFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
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
