package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

// FuzzOpenGarbageDB writes arbitrary bytes as the DB file and opens it. Open is on the statusline/warn
// hot path, so it must NEVER panic on a corrupt/truncated/garbage file: it returns a valid ReadState
// and either self-heals (StateOK, garbage quarantined) or reports a non-OK state. Regression guard for
// the fail-open corruption handling.
func FuzzOpenGarbageDB(f *testing.F) {
	seeds := [][]byte{
		[]byte("this is not a sqlite file"),
		[]byte("SQLite format 3\x00"),        // valid magic, truncated header
		{0x53, 0x51, 0x4c, 0x69, 0x74, 0x65}, // partial magic
		{},                                   // empty file
		[]byte("\x00\x00\x00\x00"),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		dbPath := filepath.Join(t.TempDir(), "ccpool.db")
		if err := os.WriteFile(dbPath, raw, 0o644); err != nil {
			t.Skip()
		}
		t.Setenv("CCPOOL_DB", dbPath)

		s, st := store.Open() // must not panic
		switch st {
		case store.StateOK:
			if s == nil {
				t.Fatal("StateOK with a nil store")
			}
			// A healed/created DB must be usable, not just non-panicking.
			if _, rst := s.Snapshots(); rst != store.StateOK {
				t.Fatalf("healed DB Snapshots state = %v", rst)
			}
			s.Close()
		case store.StateCorrupt, store.StateTransient:
			if s != nil {
				t.Fatalf("non-OK state %v returned a non-nil store", st)
				s.Close()
			}
		default:
			t.Fatalf("Open returned an unknown ReadState %v", st)
		}
	})
}

// FuzzSnapshotPayload stores an arbitrary payload as a snapshot row and reads it back. Snapshots()
// parses each payload on the fail-open render path (warn/statusline), so a hostile/garbage payload
// must never panic -- it is dropped if it doesn't parse to an object.
func FuzzSnapshotPayload(f *testing.F) {
	seeds := []string{
		`{"session_id":"s1","captured_at":1720000000,"rate_limits":{"seven_day":{"used_percentage":50}}}`,
		`{"captured_at":"not-a-number"}`,
		`[1,2,3]`, `"just a string"`, `null`, ``, `{`, `{"a":`,
		`{"captured_at":1e400}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, payload string) {
		t.Setenv("CCPOOL_DB", filepath.Join(t.TempDir(), "ccpool.db"))
		s, st := store.Open()
		if st != store.StateOK || s == nil {
			t.Skip()
		}
		defer s.Close()
		if err := s.PutSnapshot("s1", 1720000000, []byte(payload)); err != nil {
			t.Skip() // a payload SQLite can't store is not what's under test
		}
		if _, rst := s.Snapshots(); rst != store.StateOK { // must not panic; parse failures drop the row
			t.Fatalf("Snapshots state = %v on payload %q", rst, payload)
		}
	})
}
