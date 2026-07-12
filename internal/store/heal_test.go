package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

// The full auto-recovery: seed history, roll a backup, corrupt the DB header (the case that used to
// silently wipe), reopen -> Open heals to a fresh DB WITH the history restored from the .bak, and drops
// a breadcrumb so commands know a recovery happened.
func TestOpenHealsFromBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", dbPath)

	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	for i := 0; i < 25; i++ {
		if err := s.AppendHistory(store.HistoryRow{T: int64(i), Wk: float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if ran, err := s.BackupIfStale(1000, 86400); err != nil || !ran {
		t.Fatalf("backup ran=%v err=%v", ran, err)
	}
	s.Close()

	// Corrupt the DB header so the schema probe at Open fails (the auto-wipe/self-heal path).
	if err := os.WriteFile(dbPath, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reopen: heals to a fresh DB, restores history from the .bak, drops the breadcrumb.
	h, hst := store.Open()
	if hst != store.StateOK || h == nil {
		t.Fatalf("heal open = %v", hst)
	}
	defer h.Close()
	if row, rst := h.LastSessionRow(nil); rst != store.StateOK || row == nil || row.Wk != 24 {
		t.Fatalf("history not restored from backup: %+v state %v", row, rst)
	}
	if matches, _ := filepath.Glob(dbPath + ".corrupt-*"); len(matches) == 0 {
		t.Fatal("corrupt original should be quarantined, not deleted")
	}
	rows, pending := store.RecoveryPending()
	if !pending || rows != 25 {
		t.Fatalf("RecoveryPending = (%d, %v), want (25, true)", rows, pending)
	}
	store.ClearRecoveryMark()
	if _, pending := store.RecoveryPending(); pending {
		t.Fatal("breadcrumb should clear")
	}
}

// No backup present: Open still heals (empty), drops the breadcrumb with 0 restored so the user is
// still told corruption happened (and doctor can salvage the quarantine).
func TestOpenHealsWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", dbPath)

	s, _ := store.Open()
	_ = s.AppendHistory(store.HistoryRow{T: 1, Wk: 1})
	s.Close()
	if err := os.WriteFile(dbPath, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}

	h, hst := store.Open()
	if hst != store.StateOK || h == nil {
		t.Fatalf("heal open = %v", hst)
	}
	defer h.Close()
	if row, _ := h.LastSessionRow(nil); row != nil {
		t.Fatal("no backup -> empty DB expected")
	}
	if rows, pending := store.RecoveryPending(); !pending || rows != 0 {
		t.Fatalf("RecoveryPending = (%d,%v), want (0,true) so the user is still told", rows, pending)
	}
}
