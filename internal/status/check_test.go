package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/store"
)

// After a corruption heal: `check` (automation) shows a brief pointer WITHOUT consuming the breadcrumb,
// and `status` (human) shows the full notice and clears it. The regression guarded here is that an
// autonomous check must not silently eat the one recovery notice before a human sees it.
func TestRecoveryNudgeCheckDoesNotConsumeStatusClears(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ccpool.db")
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", dbPath)
	t.Setenv("CCPOOL_CONFIG", filepath.Join(dir, "no-config.json"))

	// Heal an empty store to create the breadcrumb: seed, back up, corrupt-header, reopen.
	s, _ := store.Open()
	_ = s.AppendHistory(store.HistoryRow{T: 1, Wk: 1})
	_, _ = s.BackupIfStale(1000, 86400)
	s.Close()
	if err := os.WriteFile(dbPath, make([]byte, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _ := store.Open()
	h.Close() // heals + drops the breadcrumb

	// An autonomous `check` first: points at status, must NOT clear the breadcrumb.
	chk, _ := Report(1720000000)
	cj := strings.Join(chk, "\n")
	if !strings.Contains(cj, "corrupted and rebuilt") || !strings.Contains(cj, "ccpool status") {
		t.Fatalf("check should point at status, got:\n%s", cj)
	}
	// A second check still shows it (not consumed).
	if chk2, _ := Report(1720000000); !strings.Contains(strings.Join(chk2, "\n"), "corrupted and rebuilt") {
		t.Fatal("check must not consume the recovery notice")
	}

	// Now a human `status`: full detail (restored count + quarantine), and it clears.
	sj := strings.Join(Status(1720000000), "\n")
	if !strings.Contains(sj, "history rows restored") || strings.Contains(sj, "no data yet. Wire") {
		t.Fatalf("status should show the full recovery detail:\n%s", sj)
	}
	// Cleared: check and status are now quiet.
	if chk3, _ := Report(1720000000); strings.Contains(strings.Join(chk3, "\n"), "corrupted and rebuilt") {
		t.Fatal("breadcrumb should clear after status shows it")
	}
}

// An unreadable store (locked/corrupt) is not a fresh install: Status must name it, not tell the user
// to wire up a statusline that is already wired. Regression guard for the LOUD-command misdirection.
func TestStatusUnreadableStoreIsNotFreshInstall(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "ccpool.db") // a directory at the DB path -> store.Open non-OK
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", dbDir)
	t.Setenv("CCPOOL_CONFIG", filepath.Join(dir, "no-config.json"))

	joined := strings.Join(Status(1720000000), "\n")
	if strings.Contains(joined, "no data yet") || strings.Contains(joined, "Wire `ccpool") {
		t.Errorf("unreadable store showed the fresh-install message:\n%s", joined)
	}
	if !strings.Contains(joined, "unreadable") {
		t.Errorf("unreadable store not named in output:\n%s", joined)
	}
}

// absentOrCorrupt keys off the store read state now that snapshots live in the DB: StateOK (zero rows)
// is warm-up; a non-OK store is genuinely unreadable, and we keep the truthful busy-vs-corrupt split
// rather than a false corruption alarm on a merely-busy DB.
func TestAbsentOrCorrupt(t *testing.T) {
	cases := []struct {
		st   store.ReadState
		want string
	}{
		{store.StateOK, "No usage snapshots yet"},
		{store.StateTransient, "temporarily unreadable"},
		{store.StateCorrupt, "unreadable (corrupt)"},
	}
	for _, c := range cases {
		if got := absentOrCorrupt(c.st); !strings.Contains(got, c.want) {
			t.Errorf("absentOrCorrupt(%v) = %q, want to contain %q", c.st, got, c.want)
		}
	}
}

// With snapshots and history in one store, "history unreadable while snapshots readable" can't be
// staged (an unreadable store loses both), so the burn-projection-unavailable lines are covered here
// instead of in conformance. A non-OK histState with no projection must name why burn is missing.
func TestWeeklyLinesHistStateBurnMessage(t *testing.T) {
	const now = 1720000000
	wk := pool.Window{Used: 50, Reset: now + 3*86400}
	cases := []struct {
		st   store.ReadState
		want string
	}{
		{store.StateCorrupt, "history unreadable"},
		{store.StateTransient, "history read failed"},
		{store.StateOK, ""}, // OK + no projection -> no burn line at all
	}
	for _, c := range cases {
		var lines []string
		weeklyLines(&lines, wk, true, nil, c.st, now)
		joined := strings.Join(lines, "\n")
		if c.want == "" {
			if strings.Contains(joined, "burn:") {
				t.Errorf("histState=%v: unexpected burn line in %q", c.st, joined)
			}
			continue
		}
		if !strings.Contains(joined, c.want) {
			t.Errorf("histState=%v: got %q, want to contain %q", c.st, joined, c.want)
		}
	}
}
