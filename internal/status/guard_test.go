package status

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/store"
)

// The cutover guard warns only when the user upgraded without importing: an empty history table with
// a legacy rate-limit-history.jsonl beside it. Fresh install (no JSONL) and post-import (rows present)
// stay silent.
func TestCutoverGuard(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CCPOOL_HOME", dir)
	t.Setenv("CCPOOL_DB", filepath.Join(dir, "ccpool.db"))
	legacy := filepath.Join(dir, "rate-limit-history.jsonl") // == paths.History() under CCPOOL_HOME

	open := func() *store.Store {
		t.Helper()
		s, st := store.Open()
		if st != store.StateOK || s == nil {
			t.Fatalf("open = %v", st)
		}
		return s
	}

	// 1. empty DB, no legacy file -> fresh install, silent.
	s := open()
	if _, ok := cutoverGuard(s); ok {
		t.Fatal("guard fired on a fresh install (no legacy file)")
	}
	s.Close()

	// 2. empty DB, legacy file present -> upgraded-without-import, guard fires.
	if err := os.WriteFile(legacy, []byte(`{"t":1,"wk":5}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s = open()
	g, ok := cutoverGuard(s)
	if !ok || !strings.Contains(g, "database is empty") {
		t.Fatalf("guard = %q ok=%v, want the not-imported warning", g, ok)
	}

	// 3. history has rows (imported), legacy file still present -> silent.
	reset := int64(100)
	if err := s.AppendHistory(store.HistoryRow{T: 1, Wk: 5, WkReset: &reset}); err != nil {
		t.Fatal(err)
	}
	if _, ok := cutoverGuard(s); ok {
		t.Fatal("guard fired after history was imported")
	}
	s.Close()
}

// weeklyLines' no-projection branch picks its burn message by read-state: unreadable (Corrupt),
// read-failed/retry (Transient), or silent (OK warm-up). An empty envelope forces the no-projection path.
func TestWeeklyBurnMessageByState(t *testing.T) {
	t.Setenv("CCPOOL_HOME", t.TempDir()) // isolate profile/config reads to defaults
	now := int64(1_700_000_000)
	wk := pool.Window{Used: 50, Reset: now + 3*86400}

	for _, tc := range []struct {
		state store.ReadState
		want  string // "" -> no burn: history line at all
	}{
		{store.StateOK, ""},
		{store.StateCorrupt, "history unreadable"},
		{store.StateTransient, "history read failed"},
	} {
		var lines []string
		weeklyLines(&lines, wk, true, nil, tc.state, now) // nil envelope -> no projection
		joined := strings.Join(lines, "\n")
		switch {
		case tc.want == "" && strings.Contains(joined, "burn: history"):
			t.Fatalf("StateOK should emit no burn:history line, got:\n%s", joined)
		case tc.want != "" && !strings.Contains(joined, tc.want):
			t.Fatalf("state %v: want %q in:\n%s", tc.state, tc.want, joined)
		}
	}
}
