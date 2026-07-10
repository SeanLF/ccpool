package rhythm

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The rendered `ccpool rhythm` output must be byte-identical to Ruby CCPool.rhythm. For each fixture
// we stage the SAME transcript corpus in one temp dir, point both sides' CCPOOL_PROJECTS at it, pin
// TZ + Go's time.Local to the fixture zone, and diff the output.

type rhythmFixture struct {
	Name  string            `json:"name"`
	Now   json.Number       `json:"now"`
	TZ    string            `json:"tz"`
	Env   map[string]string `json:"env"`
	Files []fileSpec        `json:"files"`
}

type fileSpec struct {
	Path   string      `json:"path"`
	Events []eventSpec `json:"events"`
	Raw    []rawSpec   `json:"raw"`
}

// eventSpec expands to `count` lines at hour h:30 UTC for each day in [day_from, day_to] and each h
// in hours — the UTC date coming from (now - day*86400), the way the Ruby test builds its corpus.
type eventSpec struct {
	DayFrom json.Number   `json:"day_from"`
	DayTo   json.Number   `json:"day_to"`
	Hours   []json.Number `json:"hours"`
	Count   json.Number   `json:"count"`
}

// rawSpec writes a verbatim timestamp `count` times (for the negative-TZ rollover case).
type rawSpec struct {
	Timestamp string      `json:"timestamp"`
	Count     json.Number `json:"count"`
}

var rhythmEnvKeys = []string{"CCPOOL_RHYTHM_WINDOW", "CCPOOL_RHYTHM_R", "CCPOOL_RHYTHM_PEAK", "CCPOOL_CLOCK"}

func TestRhythmConformance(t *testing.T) {
	if _, err := exec.LookPath("ruby"); err != nil {
		t.Skip("ruby not found; conformance diff needs the Ruby oracle")
	}
	root := repoRoot(t)
	oracle := filepath.Join(root, "conformance", "rhythm_oracle.rb")
	fixtures := loadFixtures(t, filepath.Join(root, "conformance", "rhythm_fixtures.json"))

	savedLocal := time.Local
	t.Cleanup(func() { time.Local = savedLocal })

	for _, fx := range fixtures {
		t.Run(fx.Name, func(t *testing.T) {
			for _, k := range rhythmEnvKeys {
				os.Unsetenv(k)
			}
			for k, v := range fx.Env {
				t.Setenv(k, v)
			}
			loc := loadZone(t, fx.TZ)
			time.Local = loc // pin Go's zone to the fixture TZ (rhythm renders in local time)
			t.Setenv("TZ", fx.TZ)

			now, err := fx.Now.Int64()
			if err != nil {
				t.Fatalf("bad now: %v", err)
			}

			projects := t.TempDir()
			stageCorpus(t, projects, fx, now)
			t.Setenv("CCPOOL_PROJECTS", projects)

			goOut := strings.Join(Report(now), "\n") + "\n"
			rubyOut := runOracle(t, oracle, projects, fx, now)

			if goOut != rubyOut {
				t.Errorf("rhythm mismatch\n go:   %q\n ruby: %q", goOut, rubyOut)
			}
		})
	}
}

// stageCorpus writes each fixture file's transcript lines under the projects dir.
func stageCorpus(t *testing.T, projects string, fx rhythmFixture, now int64) {
	t.Helper()
	for _, fsp := range fx.Files {
		var b strings.Builder
		for _, ev := range fsp.Events {
			dayFrom := numI(t, ev.DayFrom)
			dayTo := numI(t, ev.DayTo)
			count := numI(t, ev.Count)
			for day := dayFrom; day <= dayTo; day++ {
				date := time.Unix(now-int64(day)*86400, 0).UTC()
				for _, hn := range ev.Hours {
					h := numI(t, hn)
					stamp := date.Format("2006-01-02") + fmtHour(h)
					line := `{"timestamp":"` + stamp + `"}` + "\n"
					for i := 0; i < count; i++ {
						b.WriteString(line)
					}
				}
			}
		}
		for _, rw := range fsp.Raw {
			count := numI(t, rw.Count)
			line := `{"timestamp":"` + rw.Timestamp + `"}` + "\n"
			for i := 0; i < count; i++ {
				b.WriteString(line)
			}
		}
		full := filepath.Join(projects, fsp.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
			t.Fatalf("write corpus: %v", err)
		}
	}
}

// fmtHour renders the "THH:30:00Z" tail with a zero-padded hour (mirrors the Ruby strftime).
func fmtHour(h int) string {
	hh := "0" + itoa(h)
	return "T" + hh[len(hh)-2:] + ":30:00Z"
}

func runOracle(t *testing.T, oracle, projects string, fx rhythmFixture, now int64) string {
	t.Helper()
	in, err := json.Marshal(map[string]any{"now": fx.Now})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	env := append(os.Environ(), "CCPOOL_PROJECTS="+projects, "TZ="+fx.TZ)
	for k, v := range fx.Env {
		env = append(env, k+"="+v)
	}
	cmd := exec.Command("ruby", oracle)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("oracle failed: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String()
}

func loadFixtures(t *testing.T, path string) []rhythmFixture {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var fs []rhythmFixture
	if err := dec.Decode(&fs); err != nil {
		t.Fatalf("decode fixtures: %v", err)
	}
	return fs
}

func loadZone(t *testing.T, tz string) *time.Location {
	t.Helper()
	if tz == "" || tz == "UTC" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatalf("load zone %q: %v", tz, err)
	}
	return loc
}

func numI(t *testing.T, n json.Number) int {
	t.Helper()
	v, err := n.Int64()
	if err != nil {
		t.Fatalf("bad number %q: %v", n.String(), err)
	}
	return int(v)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
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
