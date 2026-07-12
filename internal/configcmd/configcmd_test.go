package configcmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SeanLF/ccpool/internal/profile"
)

// TestDetectFromNeverSeedsProfile is the MEDIUM-1 fix: detectFrom must NEVER seed pace.profile from
// rhythm's classification (not "workhours", not "weekdays", not "custom"). See detectFrom's doc
// comment for why -- profile.Load's "custom" branch takes the weight-vector path and bypasses
// Days/H0/H1 gating entirely, so a persisted profile:"custom" with no weights silently behaves like
// uniform 24/7 pacing regardless of the detected work_days/wake_hours.
func TestDetectFromNeverSeedsProfile(t *testing.T) {
	var hours [24]int
	hours[12] = 100
	wdays := [7]int{10, 10, 10, 10, 10, 10, 10} // every day active -> rhythm.Detect returns "workhours"

	c := detectFrom(1.0, hours, wdays)

	if c.Pace == nil {
		t.Fatal("Pace is nil, want work_days/wake_hours seeded")
	}
	if c.Pace.Profile != nil {
		t.Errorf("Pace.Profile = %q, want nil (never persist a rhythm-classified profile)", *c.Pace.Profile)
	}
}

// TestDetectFromWorkhoursPinsAllWeekWorkDays: rhythm.Detect reports an all-7-days rhythm (hour-only
// restriction) with workDays=="". detectFrom must pin Pace.WorkDays to "0-6" explicitly in that one
// case, so the persisted config states what was actually detected.
func TestDetectFromWorkhoursPinsAllWeekWorkDays(t *testing.T) {
	var hours [24]int
	hours[12] = 100                             // single sharp peak -> wakeWindow collapses to [12,13)
	wdays := [7]int{10, 10, 10, 10, 10, 10, 10} // every day active -> rhythm.Detect returns "workhours", ""

	c := detectFrom(1.0, hours, wdays)

	if c.Pace.WorkDays == nil || *c.Pace.WorkDays != "0-6" {
		t.Errorf("Pace.WorkDays = %v, want \"0-6\" (the detected all-week rhythm, restricted to the detected hours)", derefStr(c.Pace.WorkDays))
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

	if c.Pace.WorkDays == nil || *c.Pace.WorkDays != "1-5" {
		t.Errorf("Pace.WorkDays = %v, want 1-5 (rhythm's own value, not the workhours override)", derefStr(c.Pace.WorkDays))
	}
}

// TestDetectFromEvenLeavesPaceNil: a weak/no rhythm (R=0) must seed no schedule at all -- Pace
// itself stays nil (not just WorkDays/WakeHours), so profile.Load defaults cleanly and Merge can
// fill it from a user's own choice without a stray empty pace object in the way.
func TestDetectFromEvenLeavesPaceNil(t *testing.T) {
	var hours [24]int
	var wdays [7]int
	c := detectFrom(0.0, hours, wdays) // R=0 -> even, no schedule

	if c.Pace != nil {
		t.Errorf("Pace = %+v, want nil (even/no-rhythm must seed nothing)", c.Pace)
	}
}

// TestDetectFromScheduleAppliesViaGating is the MEDIUM-1 regression test proving the bug is fixed:
// an irregular (non-Mon-Fri, non-all-week) active-day subset -- exactly what rhythm.Detect labels
// "custom" -- must round-trip through profile.Load as an ACTUAL gated schedule, not silently decay
// to uniform 24/7 pacing. Before the fix, detectFrom persisted pace.profile="custom" with no
// weights, which profile.Load's custom branch reads as all-ones weight vectors (Uniform==true) --
// the detected schedule was seeded but never applied.
func TestDetectFromScheduleAppliesViaGating(t *testing.T) {
	var hours [24]int
	hours[12] = 100
	var wdays [7]int
	wdays[1], wdays[3], wdays[5] = 10, 10, 10 // Mon/Wed/Fri only -> irregular subset -> rhythm labels this "custom"

	c := detectFrom(1.0, hours, wdays)

	if c.Pace == nil || c.Pace.Profile != nil {
		t.Fatalf("Pace = %+v, want non-nil with Profile nil", c.Pace)
	}
	if c.Pace.WorkDays == nil || *c.Pace.WorkDays != "1,3,5" {
		t.Fatalf("Pace.WorkDays = %v, want the detected irregular day set 1,3,5", derefStr(c.Pace.WorkDays))
	}

	// Round-trip through profile.Load the way a real read would (file > env is irrelevant here --
	// env.String checks os env first, so setting CCPOOL_* directly simulates "this is what the file
	// holds" for profile.Load's purposes). Point CCPOOL_CONFIG at an empty temp path too, so
	// profile.Load's own fail-open file read can't pick up a real ~/.ccpool/ccpool.json.
	t.Setenv("CCPOOL_CONFIG", filepath.Join(t.TempDir(), "ccpool.json"))
	t.Setenv("CCPOOL_PACE_PROFILE", "") // detectFrom leaves profile unset; confirm Load agrees it's absent
	t.Setenv("CCPOOL_WORK_DAYS", *c.Pace.WorkDays)
	if c.Pace.WakeHours != nil {
		t.Setenv("CCPOOL_WAKE_HOURS", *c.Pace.WakeHours)
	}

	p := profile.Load()
	if p.DayWeights != nil || p.HourWeights != nil {
		t.Error("profile.Load took the weight-vector (custom) path; want the Days/H0/H1 gating path")
	}
	if !p.Scheduled() {
		t.Error("profile.Load of the detected config is Uniform (24/7) -- the detected schedule was NOT applied (this is the MEDIUM-1 bug)")
	}
	if p.Days[2] {
		t.Error("Tuesday (wday=2) is marked active, want inactive -- it is not in the detected {1,3,5} set")
	}
	for _, d := range []int{1, 3, 5} {
		if !p.Days[d] {
			t.Errorf("day %d not active, want it in the detected day set", d)
		}
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

// TestInitForceOverwritesCorruptConfig is the MEDIUM-2 fix: --force's whole point is "regenerate
// from scratch, overwrite" -- including blowing away a corrupt file, which is the exact case it
// exists for. Before the fix, Init read the existing config BEFORE checking --force and returned
// exit 2 on a Load error unconditionally, so --force couldn't recover from a corrupt file at all.
func TestInitForceOverwritesCorruptConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ccpool.json")
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_CONFIG", cfgPath)
	t.Setenv("CCPOOL_PROJECTS", filepath.Join(dir, "projects"))
	t.Setenv("CCPOOL_HISTORY", filepath.Join(dir, "h.jsonl"))

	lines, code := Init([]string{"--apply", "--force"}, 1_700_000_000)
	if code != 0 {
		t.Fatalf("Init --apply --force on a corrupt config: code = %d, want 0 (lines: %v)", code, lines)
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("Init --apply --force: %s not written: %v", cfgPath, err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Errorf("Init --apply --force wrote invalid JSON: %v\ncontent: %s", err, b)
	}
}

// TestInitApplyWithoutForceStillFailsOnCorruptConfig confirms MEDIUM-2's fix is scoped to --force:
// without it, a corrupt existing config must still fail loud (unchanged behaviour).
func TestInitApplyWithoutForceStillFailsOnCorruptConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "ccpool.json")
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_CONFIG", cfgPath)
	t.Setenv("CCPOOL_PROJECTS", filepath.Join(dir, "projects"))
	t.Setenv("CCPOOL_HISTORY", filepath.Join(dir, "h.jsonl"))

	_, code := Init([]string{"--apply"}, 1_700_000_000)
	if code != 2 {
		t.Errorf("Init --apply (no force) on a corrupt config: code = %d, want 2", code)
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
