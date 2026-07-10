package initcmd

import (
	"os"
	"path/filepath"
	"testing"
)

// These guard the never-clobber invariant for inputs the byte-diff conformance can't cover (Ruby
// aborts with a crash backtrace, so there's no byte-output to match — the contract is behavioural:
// abort non-zero, write NOTHING, leave the file exactly as it was).

func TestInitRefusesNonObjectHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := `{"permissions":{"defaultMode":"auto"},"hooks":["USER_DATA_THAT_MUST_NOT_BE_LOST"]}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_SETTINGS", path)

	if err := Run([]string{"--apply"}, 1000); err == nil {
		t.Fatal("Run(--apply) on non-object hooks returned nil; want a loud error (never-clobber)")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("settings.json was modified despite the abort\n got:  %s\n want: %s", got, original)
	}
	// No backup should have been taken (we aborted before the write path).
	if entries, _ := filepath.Glob(path + ".bak.*"); len(entries) != 0 {
		t.Errorf("a backup was written for a refused merge: %v", entries)
	}
}

func TestInitRefusesUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// A directory at the settings path: os.Stat succeeds, os.ReadFile fails -> unreadable, not fresh.
	path := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_SETTINGS", path)

	if err := Run([]string{"--apply"}, 1000); err == nil {
		t.Fatal("Run(--apply) on an unreadable settings path returned nil; want a loud error")
	}
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		t.Errorf("the settings path was altered; want the original directory intact")
	}
}
