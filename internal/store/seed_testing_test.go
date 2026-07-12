package store_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

// The seeder must insert in file/arrival order so tied timestamps keep their arrival order under the
// envelope's rowid tie-break -- else the running max would differ from the live append path.
func TestSeedHistoryJSONLPreservesArrivalOrder(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ccpool.db")
	t.Setenv("CCPOOL_DB", dbPath)

	now := int64(1_800_000_000)
	reset := now + 3*86400
	// Three rows, SAME t (tie), arrival order 25 -> 40 -> 30: running max must be 25, 40, 40.
	jsonl := ""
	for _, wk := range []int{25, 40, 30} {
		jsonl += fmt.Sprintf(`{"t":%d,"wk":%d,"wk_reset":%d,"tier":"max_20x","cost":null,"session":"s1"}`+"\n", now, wk, reset)
	}
	if err := store.SeedHistoryJSONL(dbPath, jsonl); err != nil {
		t.Fatal(err)
	}

	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = %v", st)
	}
	defer s.Close()
	rows, rs := s.EnvelopeWeekly(now + 100)
	if rs != store.StateOK {
		t.Fatalf("EnvelopeWeekly state %v", rs)
	}
	var got []float64
	for _, r := range rows {
		got = append(got, r.Value)
	}
	if !reflect.DeepEqual(got, []float64{25, 40, 40}) {
		t.Fatalf("running max = %v, want [25 40 40] (arrival order preserved)", got)
	}
}

// Sentinel rows (bench session, 9999999999 reset) and unparseable lines are skipped, matching the
// importer, so they never pollute the envelope.
func TestSeedHistoryJSONLSkipsSentinelsAndJunk(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ccpool.db")
	t.Setenv("CCPOOL_DB", dbPath)

	now := int64(1_800_000_000)
	reset := now + 3*86400
	jsonl := fmt.Sprintf(`{"t":%d,"wk":30,"wk_reset":%d,"session":"s1"}`, now, reset) + "\n" +
		`not-json` + "\n" +
		`{"wk":50}` + "\n" + // missing t
		fmt.Sprintf(`{"t":%d,"wk":99,"wk_reset":9999999999,"session":"s1"}`, now+60) + "\n" +
		fmt.Sprintf(`{"t":%d,"wk":88,"wk_reset":%d,"session":"bench"}`, now+120, reset) + "\n"
	if err := store.SeedHistoryJSONL(dbPath, jsonl); err != nil {
		t.Fatal(err)
	}

	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = %v", st)
	}
	defer s.Close()
	rows, _ := s.EnvelopeWeekly(now + 200)
	// Only the one valid non-sentinel row survives.
	if len(rows) != 1 || rows[0].Value != 30 {
		t.Fatalf("envelope = %+v, want a single row wk=30 (sentinels/junk skipped)", rows)
	}
}
