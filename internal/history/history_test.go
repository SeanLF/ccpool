package history

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

const (
	testNow   = int64(1_720_000_000)
	testReset = testNow + 3*86400 // a sane weekly reset ~3 days ahead
)

// seedDB points CCPOOL_DB at a fresh temp DB, creates the schema (so a no-op Seed still leaves a
// queryable empty table), and returns the path. The driver is registered via the store package that
// history.go imports, so a direct sql.Open works for row-count assertions.
func seedDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "ccpool.db")
	t.Setenv("CCPOOL_DB", dbPath)
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("seed store: %v", st)
	}
	_ = s.Close()
	return dbPath
}

func countHistory(t *testing.T, dbPath string) int {
	t.Helper()
	d, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM history`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// payload builds a parsed CC statusline payload the way rb.ParseObject hands it to Seed (numbers as
// json.Number). wk/ses are number literals; a "" reset/ses omits that field; sess "" omits session.
func payload(t *testing.T, wk string, wkReset int64, ses string, sesReset int64, sess string) map[string]any {
	t.Helper()
	sd := fmt.Sprintf(`"seven_day":{"used_percentage":%s,"resets_at":%d}`, wk, wkReset)
	rl := sd
	if ses != "" {
		rl += fmt.Sprintf(`,"five_hour":{"used_percentage":%s,"resets_at":%d}`, ses, sesReset)
	}
	j := fmt.Sprintf(`{"rate_limits":{%s}`, rl)
	if sess != "" {
		j += fmt.Sprintf(`,"session_id":"%s"`, sess)
	}
	j += `}`
	m := rb.ParseObject([]byte(j))
	if m == nil {
		t.Fatalf("bad test payload: %s", j)
	}
	return m
}

// mustSeed drives the Prepare-decides / caller-writes flow the statusline capture uses: build the row,
// and append it when Prepare says to. Keeps the DB-outcome tests exercising dedup/throttle/guard.
func mustSeed(t *testing.T, p map[string]any, now int64) {
	t.Helper()
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open = %v", st)
	}
	defer s.Close()
	if row, appendIt := Prepare(s, p, now); appendIt {
		if err := s.AppendHistory(row); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
}

func TestSeedFreshAppend(t *testing.T) {
	db := seedDB(t)
	mustSeed(t, payload(t, "45", testReset, "", 0, "s1"), testNow)
	if n := countHistory(t, db); n != 1 {
		t.Fatalf("fresh append: %d rows, want 1", n)
	}
}

func TestSeedNoSevenDayNoop(t *testing.T) {
	db := seedDB(t)
	m := rb.ParseObject([]byte(`{"rate_limits":{"five_hour":{"used_percentage":30,"resets_at":1720003600}}}`))
	mustSeed(t, m, testNow)
	if n := countHistory(t, db); n != 0 {
		t.Fatalf("no seven_day should no-op: %d rows, want 0", n)
	}
}

func TestSeedSameStateSkips(t *testing.T) {
	db := seedDB(t)
	p := payload(t, "45", testReset, "30", testNow+3600, "s1")
	mustSeed(t, p, testNow)
	mustSeed(t, p, testNow+1) // identical wk+wk_reset+ses -> skip
	if n := countHistory(t, db); n != 1 {
		t.Fatalf("same-state skip: %d rows, want 1", n)
	}
}

func TestSeedWkMovedAppends(t *testing.T) {
	db := seedDB(t)
	mustSeed(t, payload(t, "45", testReset, "", 0, "s1"), testNow)
	mustSeed(t, payload(t, "46", testReset, "", 0, "s1"), testNow+1) // wk moved -> append
	if n := countHistory(t, db); n != 2 {
		t.Fatalf("wk moved: %d rows, want 2", n)
	}
}

func TestSeedSesThrottledWithinInterval(t *testing.T) {
	db := seedDB(t)
	mustSeed(t, payload(t, "45", testReset, "10", testNow+3600, "s1"), testNow)
	mustSeed(t, payload(t, "45", testReset, "12", testNow+3600, "s1"), testNow+30) // only ses moved, <60s
	if n := countHistory(t, db); n != 1 {
		t.Fatalf("ses throttle: %d rows, want 1", n)
	}
}

func TestSeedSesAppendsPastInterval(t *testing.T) {
	db := seedDB(t)
	mustSeed(t, payload(t, "45", testReset, "10", testNow+3600, "s1"), testNow)
	mustSeed(t, payload(t, "45", testReset, "12", testNow+3600, "s1"), testNow+120) // ses moved, >60s
	if n := countHistory(t, db); n != 2 {
		t.Fatalf("ses append past interval: %d rows, want 2", n)
	}
}

func TestSeedPerSessionDedup(t *testing.T) {
	db := seedDB(t)
	mustSeed(t, payload(t, "45", testReset, "", 0, "A"), testNow)
	mustSeed(t, payload(t, "50", testReset, "", 0, "B"), testNow+1) // different session -> append
	mustSeed(t, payload(t, "45", testReset, "", 0, "A"), testNow+2) // session A unchanged -> skip
	if n := countHistory(t, db); n != 2 {
		t.Fatalf("per-session dedup: %d rows, want 2", n)
	}
}

// The guard nulls just the offending reset and keeps the row, so a far-future sentinel cannot become
// the envelope's latest=max(reset) and collapse the weekly window to the poison row.
func TestSeedGuardPreventsEnvelopeCollapse(t *testing.T) {
	seedDB(t)
	mustSeed(t, payload(t, "25", testReset, "", 0, "s1"), testNow)
	mustSeed(t, payload(t, "40", testReset, "", 0, "s1"), testNow+60)
	// A far-future wk_reset (the sentinel): its reset is nulled, excluding it from the weekly envelope.
	mustSeed(t, payload(t, "99", testNow+400*86400, "", 0, "s1"), testNow+120)

	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = %v", st)
	}
	defer s.Close()
	rows, rs := s.EnvelopeWeekly(testNow + 200)
	if rs != store.StateOK {
		t.Fatalf("EnvelopeWeekly state %v", rs)
	}
	if len(rows) != 2 { // the poison row (nulled wk_reset) is excluded from the window
		t.Fatalf("envelope has %d rows, want 2 (poison row excluded)", len(rows))
	}
	last := rows[len(rows)-1]
	if last.Value != 40 {
		t.Fatalf("running max = %v, want 40 (not poisoned to 99)", last.Value)
	}
	if !last.Reset.Valid || last.Reset.Int64 != testReset {
		t.Fatalf("reset = %+v, want %d (not the sentinel)", last.Reset, testReset)
	}
}

// A far-future reset on one window nulls only that reset; the row is still recorded (with the good
// window's data intact), not dropped.
func TestSeedGuardKeepsRowNullsOnlyBadReset(t *testing.T) {
	db := seedDB(t)
	// Sane weekly reset, absurd 5h reset -> row kept, wk/wk_reset intact, ses_reset nulled.
	mustSeed(t, payload(t, "45", testReset, "10", testNow+400*86400, "s1"), testNow)
	if n := countHistory(t, db); n != 1 {
		t.Fatalf("row should be kept: %d rows, want 1", n)
	}
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = %v", st)
	}
	defer s.Close()
	sess := "s1"
	last, _ := s.LastSessionRow(&sess)
	if last == nil || last.WkReset == nil || *last.WkReset != testReset {
		t.Fatalf("good weekly sample lost: %+v", last)
	}
	if last.SesReset != nil {
		t.Fatalf("absurd ses_reset should be nulled, got %v", *last.SesReset)
	}
}

func TestSeedRecordsValues(t *testing.T) {
	seedDB(t)
	m := rb.ParseObject([]byte(fmt.Sprintf(
		`{"rate_limits":{"seven_day":{"used_percentage":45.5,"resets_at":%d},"five_hour":{"used_percentage":12,"resets_at":%d}},"session_id":"s1","cost":{"total_cost_usd":6.25}}`,
		testReset, testNow+3600,
	)))
	mustSeed(t, m, testNow)
	// Read it back through the store to confirm the typed columns landed correctly.
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = %v", st)
	}
	defer s.Close()
	sess := "s1"
	last, rs := s.LastSessionRow(&sess)
	if rs != store.StateOK || last == nil {
		t.Fatalf("LastSessionRow = %+v state %v", last, rs)
	}
	if last.Wk != 45.5 || last.WkReset == nil || *last.WkReset != testReset {
		t.Fatalf("weekly recorded wrong: %+v", last)
	}
	if last.Ses == nil || *last.Ses != 12 || last.Cost == nil || *last.Cost != 6.25 {
		t.Fatalf("5h/cost recorded wrong: %+v", last)
	}
}

// Prepare snapshots the cumulative Anthropic $ (from the cached ccusage blocks) onto the row, so a
// future recalibration can use aligned delta(cost)/delta(wk%). Cache-only -- no ccusage spawn.
func TestPrepareCapturesCcusageCost(t *testing.T) {
	seedDB(t)
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("open %v", st)
	}
	defer s.Close()
	raw := `{"blocks":[{"startTime":"2026-07-01T00:00:00Z","endTime":"2026-07-01T05:00:00Z","costUSD":42,"models":["claude-opus-4-8"],"isGap":false}]}`
	blob, err := json.Marshal(map[string]any{"raw": raw, "at": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutKV("blocks", blob); err != nil {
		t.Fatal(err)
	}
	row, ok := Prepare(s, payload(t, "45", testReset, "", 0, "s1"), testNow)
	if !ok {
		t.Fatal("expected append")
	}
	if row.CcusageCost == nil || *row.CcusageCost != 42 {
		t.Fatalf("ccusage_cost not captured from cache: %+v", row.CcusageCost)
	}
}

// Claude bug #52326 leaks the resets_at epoch into used_percentage. An epoch-sized weekly % must be
// dropped at ingest (mirrors pool.GetWindow's >=10000 guard) so it never poisons calibration -- which,
// with monotonic-max reconstruction, would lock the spike in for a whole window.
func TestSeedDropsEpochLeakedWk(t *testing.T) {
	db := seedDB(t)
	mustSeed(t, payload(t, "1783720704", testReset, "", 0, "s1"), testNow)
	if n := countHistory(t, db); n != 0 {
		t.Fatalf("epoch-leaked wk should be dropped: %d rows, want 0", n)
	}
}

// A weekly % just over 100 is a minor overshoot, not an epoch leak; clamp to 100 (mirrors pool's
// min(u,100)) rather than dropping the reading.
func TestSeedClampsOvershootWk(t *testing.T) {
	seedDB(t)
	mustSeed(t, payload(t, "101", testReset, "", 0, "s1"), testNow)
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = %v", st)
	}
	defer s.Close()
	sess := "s1"
	last, _ := s.LastSessionRow(&sess)
	if last == nil || last.Wk != 100 {
		t.Fatalf("wk 101 should clamp to 100: %+v", last)
	}
}

// A five-hour epoch leak nulls just the 5h value and keeps the good weekly row (mirrors the
// null-just-the-bad-field reset guard), rather than dropping the whole reading.
func TestSeedNullsEpochLeakedSes(t *testing.T) {
	seedDB(t)
	mustSeed(t, payload(t, "45", testReset, "1783720704", testNow+3600, "s1"), testNow)
	s, st := store.Open()
	if st != store.StateOK || s == nil {
		t.Fatalf("Open = %v", st)
	}
	defer s.Close()
	sess := "s1"
	last, _ := s.LastSessionRow(&sess)
	if last == nil || last.Ses != nil {
		t.Fatalf("epoch-leaked ses should be nulled: %+v", last)
	}
}
