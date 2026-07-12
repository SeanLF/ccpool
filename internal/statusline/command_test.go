package statusline

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

func histCount(t *testing.T, dbPath string) int {
	t.Helper()
	d, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow("SELECT count(*) FROM history").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// PruneCaches deletes snapshot rows older than CCPOOL_CACHE_KEEP_SECS via the threaded store (no file
// sweep). A row past the keep window goes; a fresh one stays; the deleted count is returned.
func TestPruneCachesDeletesStaleRows(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	t.Setenv("CCPOOL_CACHE_KEEP_SECS", "3600")

	now := int64(1_800_000_000)
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	defer s.Close()
	// old (2h ago, past the 1h keep) and fresh (1m ago) snapshots, distinct sessions.
	if err := s.PutSnapshot("old", now-7200, []byte(`{"session_id":"old"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSnapshot("fresh", now-60, []byte(`{"session_id":"fresh"}`)); err != nil {
		t.Fatal(err)
	}

	if n := PruneCaches(s, now); n != 1 {
		t.Fatalf("PruneCaches removed %d, want 1 (the stale row)", n)
	}
	if snaps, _ := s.Snapshots(); len(snaps) != 1 {
		t.Fatalf("remaining snapshots = %d, want 1 (the fresh row)", len(snaps))
	}
	// nil store fails open to 0.
	if n := PruneCaches(nil, now); n != 0 {
		t.Fatalf("PruneCaches(nil) = %d, want 0", n)
	}
}

// capture writes the snapshot and (when not deduped) the paired history row in one store txn; a
// repeat identical render dedups the history but still upserts the snapshot.
func TestCaptureWritesSnapshotAndHistory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_HOME", dir) // isolate: never touch the dev's real ~/.ccpool
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_DB", dbPath)

	now := int64(1_800_000_000)
	reset := now + 3*86400
	raw := []byte(fmt.Sprintf(`{"session_id":"s1","rate_limits":{"seven_day":{"used_percentage":45,"resets_at":%d}}}`, reset))
	data := rb.ParseObject(raw)

	// capture takes the invocation's open store now (Command opens it once and threads it in).
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	defer s.Close()

	capture(s, raw, data, now)

	snaps, sst := s.Snapshots()
	if sst != store.StateOK || len(snaps) != 1 {
		t.Fatalf("snapshots = %d state %v, want 1", len(snaps), sst)
	}
	if _, ok := snaps[0]["rate_limits"]; !ok {
		t.Fatalf("snapshot missing rate_limits: %#v", snaps[0])
	}
	if c, ok := snaps[0]["captured_at"].(json.Number); !ok || c.String() != "1800000000" {
		t.Fatalf("captured_at = %#v, want json.Number 1800000000", snaps[0]["captured_at"])
	}
	last, lst := s.LastSessionRow(nil)
	if lst != store.StateOK || last == nil || last.Wk != 45 {
		t.Fatalf("paired history row = %+v state %v", last, lst)
	}
	if n := histCount(t, dbPath); n != 1 {
		t.Fatalf("history rows = %d, want 1", n)
	}

	// Identical second render: history deduped (still 1), snapshot upserted (still 1 session).
	capture(s, raw, data, now+1)
	if n := histCount(t, dbPath); n != 1 {
		t.Fatalf("after duplicate capture history = %d, want 1 (deduped)", n)
	}
	if snaps, _ := s.Snapshots(); len(snaps) != 1 {
		t.Fatalf("snapshots after upsert = %d, want 1", len(snaps))
	}
}
