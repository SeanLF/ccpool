package store_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SeanLF/ccpool/internal/store"
)

func freshStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("CCPOOL_DB", filepath.Join(t.TempDir(), "ccpool.db"))
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = state %v store nil=%v", st, s == nil)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestEnvelopeWeeklyRunningMax(t *testing.T) {
	s := freshStore(t)
	now := int64(1_800_000_000)
	reset := now + 3*86400
	for _, wk := range []float64{25, 40, 30} { // arrival-order running max -> 25, 40, 40
		if err := s.AppendHistory(store.HistoryRow{T: now, Wk: wk, WkReset: &reset}); err != nil {
			t.Fatal(err)
		}
		now += 60
	}
	rows, st := s.EnvelopeWeekly(now)
	if st != store.StateOK {
		t.Fatalf("state %v", st)
	}
	var got []float64
	for _, r := range rows {
		got = append(got, r.Value)
	}
	if !reflect.DeepEqual(got, []float64{25, 40, 40}) {
		t.Fatalf("running max = %v, want [25 40 40]", got)
	}
	// The interface{} reset column must normalize to a valid sql.NullInt64 carrying the latest reset.
	for i, r := range rows {
		if !r.Reset.Valid || r.Reset.Int64 != reset {
			t.Fatalf("row %d reset = %+v, want valid %d", i, r.Reset, reset)
		}
	}
}

func TestEnvelopeFiveHourRunningMax(t *testing.T) {
	s := freshStore(t)
	now := int64(1_800_000_000)
	reset := now + 3600
	for _, v := range []float64{5, 12, 8} { // running max -> 5, 12, 12
		ses := v
		if err := s.AppendHistory(store.HistoryRow{T: now, Wk: 1, WkReset: &reset, Ses: &ses, SesReset: &reset}); err != nil {
			t.Fatal(err)
		}
		now += 30
	}
	rows, st := s.EnvelopeFiveHour(now)
	if st != store.StateOK {
		t.Fatalf("state %v", st)
	}
	var got []float64
	for _, r := range rows {
		got = append(got, r.Value)
	}
	if !reflect.DeepEqual(got, []float64{5, 12, 12}) {
		t.Fatalf("running max = %v, want [5 12 12]", got)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := freshStore(t)
	// A realistic Claude Code statusline payload, captured_at already spliced (as capture does).
	payload := []byte(`{"rate_limits":{"seven_day":{"used_percentage":45,"resets_at":1700500000}},"session_id":"abc","captured_at":1700000000}`)
	if err := s.PutSnapshot("abc", 1700000000, payload); err != nil {
		t.Fatal(err)
	}
	snaps, st := s.Snapshots()
	if st != store.StateOK || len(snaps) != 1 {
		t.Fatalf("Snapshots = %d rows, state %v", len(snaps), st)
	}
	m := snaps[0]
	// captured_at is a json.Number (from the payload), so pool.DataAge/GetWindow's rb.Num reads it.
	if c, ok := m["captured_at"].(json.Number); !ok || c.String() != "1700000000" {
		t.Fatalf("captured_at = %#v, want json.Number 1700000000", m["captured_at"])
	}
	if _, ok := m["rate_limits"].(map[string]any); !ok {
		t.Fatalf("rate_limits not parsed: %#v", m["rate_limits"])
	}
	// UPSERT: a second capture for the same session replaces, not duplicates.
	if err := s.PutSnapshot("abc", 1700000100, payload); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := s.Snapshots(); len(snaps) != 1 {
		t.Fatalf("after upsert got %d snapshots, want 1", len(snaps))
	}
}

func TestKVRoundTrip(t *testing.T) {
	s := freshStore(t)
	if _, ok, st := s.GetKV("calibration"); ok || st != store.StateOK {
		t.Fatalf("cold cache: ok=%v st=%v, want ok=false StateOK", ok, st)
	}
	if err := s.PutKV("calibration", []byte(`{"dpp":24.4,"at":1700}`)); err != nil {
		t.Fatal(err)
	}
	v, ok, st := s.GetKV("calibration")
	if st != store.StateOK || !ok || string(v) != `{"dpp":24.4,"at":1700}` {
		t.Fatalf("GetKV = %q ok=%v st=%v", v, ok, st)
	}
	if err := s.PutKV("calibration", []byte(`{"dpp":30}`)); err != nil { // upsert
		t.Fatal(err)
	}
	if v, _, _ := s.GetKV("calibration"); string(v) != `{"dpp":30}` {
		t.Fatalf("after upsert GetKV = %q", v)
	}
}

func TestCaptureAndAppendAtomic(t *testing.T) {
	s := freshStore(t)
	reset := int64(1700500000)
	payload := []byte(`{"x":1,"captured_at":1700000000}`)
	err := s.CaptureAndAppend("sess", 1700000000, payload,
		store.HistoryRow{T: 1700000000, Wk: 50, WkReset: &reset, Session: strptr("sess")})
	if err != nil {
		t.Fatal(err)
	}
	// Both the snapshot and its paired history row are present after the single txn.
	if snaps, _ := s.Snapshots(); len(snaps) != 1 {
		t.Fatalf("snapshot count = %d, want 1", len(snaps))
	}
	last, st := s.LastSessionRow(nil)
	if st != store.StateOK || last == nil {
		t.Fatalf("LastSessionRow = %+v state %v", last, st)
	}
	if last.Wk != 50 || last.WkReset == nil || *last.WkReset != reset {
		t.Fatalf("paired history row = %+v", last)
	}
}

func TestLastSessionRowTypedAndFiltered(t *testing.T) {
	s := freshStore(t)
	r1, r2 := int64(1700500000), int64(1700600000)
	must(t, s.AppendHistory(store.HistoryRow{T: 100, Wk: 10, WkReset: &r1, Session: strptr("A")}))
	must(t, s.AppendHistory(store.HistoryRow{T: 200, Wk: 20, WkReset: &r2, Session: strptr("B")}))

	last, st := s.LastSessionRow(nil) // overall latest = B
	if st != store.StateOK || last == nil || last.Session == nil || *last.Session != "B" || last.Wk != 20 {
		t.Fatalf("overall last = %+v state %v", last, st)
	}
	lastA, _ := s.LastSessionRow(strptr("A")) // filtered to session A
	if lastA == nil || lastA.Wk != 10 || *lastA.WkReset != r1 {
		t.Fatalf("session A last = %+v", lastA)
	}

	empty := freshStore(t) // no rows -> nil row, StateOK (not an error)
	if row, est := empty.LastSessionRow(nil); row != nil || est != store.StateOK {
		t.Fatalf("empty LastSessionRow = %+v state %v", row, est)
	}
}

func TestDataAge(t *testing.T) {
	s := freshStore(t)
	if _, ok, st := s.DataAge(2000); ok || st != store.StateOK {
		t.Fatalf("empty DataAge: ok=%v st=%v, want ok=false StateOK", ok, st)
	}
	must(t, s.PutSnapshot("x", 1500, []byte(`{"captured_at":1500}`)))
	must(t, s.PutSnapshot("y", 1800, []byte(`{"captured_at":1800}`)))
	age, ok, st := s.DataAge(2000) // freshest is 1800 -> age 200
	if st != store.StateOK || !ok || age != 200 {
		t.Fatalf("DataAge = %d ok=%v st=%v, want 200 true OK", age, ok, st)
	}
}

func TestPruneHistoryAndSnapshots(t *testing.T) {
	s := freshStore(t)
	reset := int64(1700500000)
	for _, ts := range []int64{100, 200, 300} {
		must(t, s.AppendHistory(store.HistoryRow{T: ts, Wk: 1, WkReset: &reset}))
	}
	n, err := s.PruneHistory(250) // deletes t < 250 -> rows at 100, 200
	if err != nil || n != 2 {
		t.Fatalf("PruneHistory = %d err=%v, want 2", n, err)
	}
	must(t, s.PutSnapshot("a", 100, []byte(`{"captured_at":100}`)))
	must(t, s.PutSnapshot("b", 300, []byte(`{"captured_at":300}`)))
	sn, err := s.PruneSnapshots(250) // deletes captured_at < 250 -> a
	if err != nil || sn != 1 {
		t.Fatalf("PruneSnapshots = %d err=%v, want 1", sn, err)
	}
}

func strptr(s string) *string { return &s }

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
