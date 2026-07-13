package initcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bundled checking-usage skill: init installs it into the skills dir on --apply, but never
// clobbers a copy that's already there (its wording is the user's to edit). These behaviours aren't
// in the settings.json byte-diff conformance, so they're guarded directly here.

// skillHome stages a hermetic init run: a temp settings path + store home, an isolated skills dir,
// and the embedded skill content pinned to a known fixture. Returns the skills dir.
func skillHome(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CCPOOL_SETTINGS", filepath.Join(dir, "settings.json"))
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	skillsDir := filepath.Join(dir, "skills")
	t.Setenv("CCPOOL_SKILLS_DIR", skillsDir)

	prev := skillContent
	skillContent = []byte(content)
	t.Cleanup(func() { skillContent = prev })
	return skillsDir
}

func TestInitInstallsSkill(t *testing.T) {
	const body = "---\nname: checking-usage\n---\nRun `ccpool check`.\n"
	skillsDir := skillHome(t, body)

	if err := Run([]string{"--apply"}, 1000); err != nil {
		t.Fatalf("Run(--apply): %v", err)
	}

	got, err := os.ReadFile(filepath.Join(skillsDir, "checking-usage", "SKILL.md"))
	if err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	if string(got) != body {
		t.Errorf("installed skill content mismatch\n got:  %q\n want: %q", got, body)
	}
}

func TestInitNeverClobbersSkill(t *testing.T) {
	skillsDir := skillHome(t, "---\nname: checking-usage\n---\nEMBEDDED default\n")

	// A user who has already installed + edited the skill.
	path := filepath.Join(skillsDir, "checking-usage", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const userEdited = "my own custom wording\n"
	if err := os.WriteFile(path, []byte(userEdited), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{"--apply"}, 1000); err != nil {
		t.Fatalf("Run(--apply): %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != userEdited {
		t.Errorf("existing skill was clobbered\n got:  %q\n want: %q", got, userEdited)
	}
}

func TestInitDryRunShowsSkillWritesNothing(t *testing.T) {
	skillsDir := skillHome(t, "body\n")

	out, code := captureStdout(func() error { return Run(nil, 1000) }) // no --apply = dry run
	if code != 0 {
		t.Fatalf("dry run exit %d", code)
	}
	if !strings.Contains(out, "skill "+skillName) {
		t.Errorf("dry-run plan is missing the skill line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(skillsDir, skillName, "SKILL.md")); !os.IsNotExist(err) {
		t.Errorf("dry run must not write the skill file (stat err = %v)", err)
	}
}

func TestSkillAbsentCountsAsChange(t *testing.T) {
	// A settings.json already fully wired for ccpool (statusline + both warn hooks).
	const wired = `{"statusLine":{"type":"command","command":"ccpool statusline"},` +
		`"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"ccpool warn"}]}],` +
		`"PostToolUse":[{"hooks":[{"type":"command","command":"ccpool warn"}]}]}}`
	v, err := decodeOrdered([]byte(wired))
	if err != nil {
		t.Fatal(err)
	}
	settings := v.(*omap)

	if pl := makePlan(settings, false, true /*skillAbsent*/); !pl.changes() {
		t.Error("fully wired but skill absent should still count as a change (init installs the skill)")
	}
	if pl := makePlan(settings, false, false /*skillAbsent*/); pl.changes() {
		t.Error("fully wired and skill already present should be no change")
	}
}

// A run that only needs to install the skill (settings.json already fully wired) must not take a
// spurious backup or rewrite settings.json -- it should touch only the skills dir.
func TestSkillOnlyApplyLeavesSettingsAlone(t *testing.T) {
	skillsDir := skillHome(t, "body\n")
	settingsPath := os.Getenv("CCPOOL_SETTINGS")
	const wired = `{"statusLine":{"type":"command","command":"ccpool statusline"},` +
		`"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"ccpool warn"}]}],` +
		`"PostToolUse":[{"hooks":[{"type":"command","command":"ccpool warn"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(wired), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Run([]string{"--apply"}, 1000); err != nil {
		t.Fatalf("Run(--apply): %v", err)
	}

	if _, err := os.Stat(filepath.Join(skillsDir, skillName, "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
	if baks, _ := filepath.Glob(settingsPath + ".bak.*"); len(baks) != 0 {
		t.Errorf("skill-only apply took a spurious settings backup: %v", baks)
	}
	if got, _ := os.ReadFile(settingsPath); string(got) != wired {
		t.Errorf("settings.json was rewritten on a skill-only apply\n got:  %s\n want: %s", got, wired)
	}
}
