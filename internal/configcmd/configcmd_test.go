package configcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDetectFromWorkhoursPinsAllWeekWorkDays is the Task 7 cross-task fix: rhythm.Detect reports
// "workhours" with workDays=="" for an all-7-days rhythm (hour-only restriction). But
// internal/profile.Load's "workhours" preset defaults CCPOOL_WORK_DAYS to Mon-Fri when unset,
// which would silently narrow the detected all-week rhythm the moment this seeded config is read.
// detectFrom must pin Pace.WorkDays to "0-6" explicitly in that one case.
func TestDetectFromWorkhoursPinsAllWeekWorkDays(t *testing.T) {
	var hours [24]int
	hours[12] = 100                             // single sharp peak -> wakeWindow collapses to [12,13)
	wdays := [7]int{10, 10, 10, 10, 10, 10, 10} // every day active -> rhythm.Detect returns "workhours", ""

	c := detectFrom(1.0, hours, wdays)

	if c.Pace == nil || c.Pace.Profile == nil || *c.Pace.Profile != "workhours" {
		t.Fatalf("Pace.Profile = %v, want workhours", c.Pace)
	}
	if c.Pace.WorkDays == nil || *c.Pace.WorkDays != "0-6" {
		t.Errorf("Pace.WorkDays = %v, want \"0-6\" (else profile.Load's workhours preset narrows to Mon-Fri)", derefStr(c.Pace.WorkDays))
	}
	if c.Pace.WakeHours == nil || *c.Pace.WakeHours != "12-13" {
		t.Errorf("Pace.WakeHours = %v, want 12-13", derefStr(c.Pace.WakeHours))
	}
}

// TestDetectFromWeekdaysUsesRhythmsOwnWorkDays confirms the "0-6" pin is scoped to the
// workhours+empty-workDays case only: a weekdays (Mon-Fri) detection must carry rhythm's own value
// through untouched, not get overridden.
func TestDetectFromWeekdaysUsesRhythmsOwnWorkDays(t *testing.T) {
	var hours [24]int
	hours[12] = 100
	var wdays [7]int
	wdays[1], wdays[2], wdays[3], wdays[4], wdays[5] = 10, 10, 10, 10, 10 // Mon-Fri only

	c := detectFrom(1.0, hours, wdays)

	if c.Pace.Profile == nil || *c.Pace.Profile != "weekdays" {
		t.Fatalf("Pace.Profile = %v, want weekdays", derefStr(c.Pace.Profile))
	}
	if c.Pace.WorkDays == nil || *c.Pace.WorkDays != "1-5" {
		t.Errorf("Pace.WorkDays = %v, want 1-5 (rhythm's own value, not the workhours override)", derefStr(c.Pace.WorkDays))
	}
}

// TestDetectFromEvenLeavesScheduleFieldsNil: a weak/no rhythm (R=0) must seed a plain `even`
// profile with WorkDays/WakeHours left nil, not empty-string pointers (Merge / omitempty rely on
// absent-vs-empty, per the Config pointer-field contract).
func TestDetectFromEvenLeavesScheduleFieldsNil(t *testing.T) {
	var hours [24]int
	var wdays [7]int
	c := detectFrom(0.0, hours, wdays) // R=0 -> even, no schedule

	if c.Pace.Profile == nil || *c.Pace.Profile != "even" {
		t.Fatalf("Pace.Profile = %v, want even", derefStr(c.Pace.Profile))
	}
	if c.Pace.WorkDays != nil || c.Pace.WakeHours != nil {
		t.Errorf("even profile: WorkDays=%v WakeHours=%v, want both nil", derefStr(c.Pace.WorkDays), derefStr(c.Pace.WakeHours))
	}
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestInitDryRunWritesNothing: `config init` without --apply must print a DRY RUN plan and leave
// the config file untouched, even though it has already computed the full detected+merged result.
func TestInitDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ccpool.json")
	t.Setenv("CCPOOL_CONFIG", cfgPath)
	t.Setenv("CCPOOL_PROJECTS", filepath.Join(dir, "projects")) // absent -> no transcripts, fast + deterministic
	t.Setenv("CCPOOL_HISTORY", filepath.Join(dir, "h.jsonl"))

	lines, code := Init(nil, 1_700_000_000)
	if code != 0 {
		t.Fatalf("Init dry-run: code = %d, want 0 (lines: %v)", code, lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "DRY RUN") {
		t.Errorf("Init dry-run: output missing DRY RUN marker: %v", lines)
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Errorf("Init dry-run wrote %s (err=%v), want no file", cfgPath, err)
	}
}

// TestInitApplyWritesFile is the --apply counterpart: the same plan gets written to disk.
func TestInitApplyWritesFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ccpool.json")
	t.Setenv("CCPOOL_CONFIG", cfgPath)
	t.Setenv("CCPOOL_PROJECTS", filepath.Join(dir, "projects"))
	t.Setenv("CCPOOL_HISTORY", filepath.Join(dir, "h.jsonl"))

	lines, code := Init([]string{"--apply"}, 1_700_000_000)
	if code != 0 {
		t.Fatalf("Init --apply: code = %d, want 0 (lines: %v)", code, lines)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("Init --apply: %s not written: %v", cfgPath, err)
	}
}

// TestShowReportsCorruptConfigLoud: Show is on-demand, so a corrupt config file must fail LOUD
// (non-zero exit), unlike the hot-path hooks which silently fall back to defaults.
func TestShowReportsCorruptConfigLoud(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ccpool.json")
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_CONFIG", cfgPath)

	_, code := Show(1_700_000_000)
	if code == 0 {
		t.Error("Show on a corrupt config file returned code 0, want non-zero (fail loud)")
	}
}

// TestShowIncludesEnabledAndSourceColumns exercises the columns the config.txtar e2e script also
// checks for, at the unit level.
func TestShowIncludesEnabledAndSourceColumns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_CONFIG", filepath.Join(dir, "ccpool.json")) // absent -> defaults

	lines, code := Show(1_700_000_000)
	if code != 0 {
		t.Fatalf("Show: code = %d, want 0 (lines: %v)", code, lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "enabled") {
		t.Error("Show output missing an \"enabled\" row")
	}
	if !strings.Contains(joined, "source") {
		t.Error("Show output missing a \"source\" column header")
	}
}
