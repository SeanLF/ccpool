package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

// A pre-migration DB (history without ccusage_cost, user_version 0) must gain the column and be marked
// v1 when opened -- existing on-disk DBs upgrade in place, additively, without losing data.
func TestMigrationAddsCcusageCostColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE history (
		id INTEGER PRIMARY KEY, t INTEGER NOT NULL, wk REAL NOT NULL,
		wk_reset INTEGER, ses REAL, ses_reset INTEGER, cost REAL, session TEXT);
		INSERT INTO history (t, wk) VALUES (1, 5);
		PRAGMA user_version = 0;`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	t.Setenv("CCPOOL_DB", path)
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open (migrate) = %v", st)
	}
	_ = s.Close()

	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var ver int
	if err := check.QueryRow("PRAGMA user_version").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 1 {
		t.Fatalf("user_version = %d, want 1 after migration", ver)
	}
	if !hasCol(t, check, "history", "ccusage_cost") {
		t.Fatal("ccusage_cost column was not added by migration")
	}
	var n int // pre-existing data preserved
	if err := check.QueryRow("SELECT count(*) FROM history").Scan(&n); err != nil || n != 1 {
		t.Fatalf("row count = %d (err %v), want 1 preserved", n, err)
	}
}

func hasCol(t *testing.T, d *sql.DB, table, col string) bool {
	t.Helper()
	rows, err := d.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == col {
			return true
		}
	}
	return false
}
