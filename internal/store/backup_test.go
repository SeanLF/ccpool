package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

// Backup writes a last-known-good copy via VACUUM INTO; BackupIfStale gates on the kv timestamp.
func TestBackupIfStaleGatesAndRolls(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_DB", dbPath)

	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	defer s.Close()
	if err := s.AppendHistory(store.HistoryRow{T: 1, Wk: 5}); err != nil {
		t.Fatal(err)
	}

	// First run rolls the backup and leaves no temp behind.
	if ran, err := s.BackupIfStale(1000, 86400); err != nil || !ran {
		t.Fatalf("first BackupIfStale ran=%v err=%v, want ran=true", ran, err)
	}
	if _, err := os.Stat(dbPath + ".bak"); err != nil {
		t.Fatalf(".bak not created: %v", err)
	}
	if tmps, _ := filepath.Glob(dbPath + ".bak.*.tmp"); len(tmps) != 0 {
		t.Fatalf("backup temp files should be renamed away, found %v", tmps)
	}

	// Within the interval -> gated (no run). Past it -> runs again.
	if ran, _ := s.BackupIfStale(1001, 86400); ran {
		t.Fatal("BackupIfStale within interval should be gated")
	}
	if ran, _ := s.BackupIfStale(1000+86401, 86400); !ran {
		t.Fatal("BackupIfStale past interval should run")
	}

	// The backup is a valid, openable DB carrying the history (last-known-good, not a raw byte copy).
	t.Setenv("CCPOOL_DB", dbPath+".bak")
	b, bst := store.Open()
	if bst != store.StateOK || b == nil {
		t.Fatalf("open .bak = %v", bst)
	}
	defer b.Close()
	if row, rst := b.LastSessionRow(nil); rst != store.StateOK || row == nil || row.Wk != 5 {
		t.Fatalf("backup missing history row: %+v state %v", row, rst)
	}
}
