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

	capture(raw, data, now)

	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
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
	s.Close()
	if n := histCount(t, dbPath); n != 1 {
		t.Fatalf("history rows = %d, want 1", n)
	}

	// Identical second render: history deduped (still 1), snapshot upserted (still 1 session).
	capture(raw, data, now+1)
	if n := histCount(t, dbPath); n != 1 {
		t.Fatalf("after duplicate capture history = %d, want 1 (deduped)", n)
	}
	s2, _ := store.Open()
	defer s2.Close()
	if snaps, _ := s2.Snapshots(); len(snaps) != 1 {
		t.Fatalf("snapshots after upsert = %d, want 1", len(snaps))
	}
}
