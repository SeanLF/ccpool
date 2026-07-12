package initcmd

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/SeanLF/ccpool/internal/golden"
	"github.com/SeanLF/ccpool/internal/store"
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
var initEnvKeys = []string{"CCPOOL_CCUSAGE_CMD", "CCPOOL_SETTINGS", "CCPOOL_HOME", "CCPOOL_DB"}

func TestInitConformance(t *testing.T) {
	// Pin the zone (the preview's fmt_dur/pace math is local-zone sensitive; goldens captured under UTC).
	time.Local = time.UTC
	t.Setenv("TZ", "UTC")

	root := repoRoot(t)

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
			dir := t.TempDir()
			settingsPath := filepath.Join(dir, "settings.json")
			t.Setenv("CCPOOL_SETTINGS", settingsPath)
			// Isolate the store to an empty temp home so the statusline preview finds no snapshot and
			// prints only its header (the preview render itself isn't under test here), never the dev's
			// real ~/.ccpool. The DB path stays absent -> store.Open creates an empty DB -> no snapshot.
			t.Setenv("CCPOOL_HOME", dir)
			t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}

			stageFixture(t, fx, dir, settingsPath)
			goOut, goCode := captureStdout(func() error { return Run(fx.Argv, now) })
			goSym, goExists, goBody := inspect(settingsPath)

			golden.Assert(t, filepath.Join(root, "conformance", "golden", "initcmd", fx.Name+".init.txt"),
				normInitPaths(initEnvelope(goOut, goCode, goSym, goExists, goBody), dir))
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

// Prune deletes rows older than keepDays. The store makes this a DELETE, so we assert against the DB's
// own ground truth: the removed count equals the rows below the cutoff, and none remain below it.
func TestPruneHistoryConformance(t *testing.T) {
	root := repoRoot(t)

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

			dir := t.TempDir()
			dbPath := filepath.Join(dir, "ccpool.db")
			t.Setenv("CCPOOL_DB", dbPath)
			t.Setenv("CCPOOL_HOME", dir)
			if err := store.SeedHistoryJSONL(dbPath, fx.Hist); err != nil {
				t.Fatalf("seed history: %v", err)
			}

			cutoff := now - int64(keepDays*86400)
			before := countHist(t, dbPath, nil)
			wantRemoved := 0
			if keepDays > 0 { // keepDays<=0 keeps everything
				wantRemoved = countHist(t, dbPath, &cutoff)
			}

			s, st := store.Open()
			if st != store.StateOK || s == nil {
				t.Fatalf("open = %v", st)
			}
			removed, err := PruneHistory(s, now, keepDays)
			s.Close()
			if err != nil {
				t.Fatalf("PruneHistory: %v", err)
			}
			if removed != wantRemoved {
				t.Fatalf("removed = %d, want %d", removed, wantRemoved)
			}
			if after := countHist(t, dbPath, nil); after != before-wantRemoved {
				t.Fatalf("after prune %d rows, want %d", after, before-wantRemoved)
			}
			if keepDays > 0 {
				if below := countHist(t, dbPath, &cutoff); below != 0 {
					t.Fatalf("%d rows below cutoff still present", below)
				}
			}
		})
	}
}

// countHist counts history rows in the DB; when below != nil, only rows with t < *below.
func countHist(t *testing.T, dbPath string, below *int64) int {
	t.Helper()
	d, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()
	q := "SELECT count(*) FROM history"
	var args []any
	if below != nil {
		q += " WHERE t < ?"
		args = append(args, *below)
	}
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func (fx pruneFixture) parseKeepDays() (float64, error) {
	var f float64
	if err := json.Unmarshal([]byte(fx.KeepDays), &f); err != nil {
		return 0, err
	}
	return f, nil
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

// initEnvelope serializes the init result the same way init_oracle.rb does (NUL-separated:
// stdout, exit code, is-symlink bit, exists bit, settings bytes) so one golden captures every field.
func initEnvelope(out string, code int, isSymlink, exists bool, body []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString(out)
	buf.WriteByte(0)
	buf.WriteString(strconv.Itoa(code))
	buf.WriteByte(0)
	buf.WriteString(bit(isSymlink))
	buf.WriteByte(0)
	buf.WriteString(bit(exists))
	buf.WriteByte(0)
	buf.Write(body)
	return buf.Bytes()
}

func bit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// normInitPaths tokenizes the per-run temp settings dir (in both its /var and macOS-resolved
// /private/var forms) so the init golden is reproducible: the volatile dir is the only
// machine-specific content in init's stdout ("wiring plan for <path>", "real target: <path>"). The
// same substitution runs on both sides, so a diff in any real content still surfaces. The resolved
// form (a prefix superset of dir) must be replaced first, else the plain-dir pass mangles it.
func normInitPaths(b []byte, dir string) []byte {
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		b = bytes.ReplaceAll(b, []byte(real), []byte("<DIR>"))
	}
	return bytes.ReplaceAll(b, []byte(dir), []byte("<DIR>"))
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
