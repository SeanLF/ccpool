package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

func TestOpenCreatesFreshDB(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open fresh = state %v, store nil=%v", st, s == nil)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(dir, "ccpool.db")); err != nil {
		t.Fatalf("db not created: %v", err)
	}
}

func TestOpenSelfHealsCorruptDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_DB", dbPath)
	if err := os.WriteFile(dbPath, []byte("this is not a sqlite file"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open corrupt should self-heal to OK, got state %v store nil=%v", st, s == nil)
	}
	defer s.Close()
	matches, _ := filepath.Glob(dbPath + ".corrupt-*")
	if len(matches) == 0 {
		t.Fatal("expected corrupt file quarantined aside")
	}
	// The recreated DB must be usable: a fresh Open over the healed file is StateOK again.
	s2, st2 := store.Open()
	if st2 != store.StateOK || s2 == nil {
		t.Fatalf("reopen after heal = %v", st2)
	}
	s2.Close()
}

// A brand-new home dir that does not exist yet must be created, not error.
func TestOpenCreatesMissingHomeDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist")
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open into missing dir = %v", st)
	}
	s.Close()
}

// A path with URI-special characters (%, #) must resolve to the literal file, not be reinterpreted
// by SQLite's URI parser into a different path. Regression guard for the DSN construction.
func TestOpenResolvesURISpecialCharPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cc%20po#l.db")
	t.Setenv("CCPOOL_DB", dbPath)
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open weird path = %v", st)
	}
	defer s.Close()
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db not created at the literal path %q: %v", dbPath, err)
	}
}
