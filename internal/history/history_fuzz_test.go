package history

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

// FuzzSeed fuzzes the history Prepare/dedup/guard path. Prepare runs on every render (fail-open hot
// path via the statusline capture), so no input may panic. Both fuzz inputs are decoded via
// rb.ParseObject and fed through Prepare+AppendHistory (the first as random pre-existing state, the
// second as the row under test), exercising the LastSessionRow/skip/guard path against a real temp DB
// reset each iteration.
func FuzzSeed(f *testing.F) {
	dbPath := filepath.Join(f.TempDir(), "ccpool.db")
	f.Setenv("CCPOOL_DB", dbPath)

	// Seeds: (pre-existing-state payload, row-under-test payload), both CC-shaped JSON.
	type seed struct{ pre, payload string }
	seeds := []seed{
		{"", `{"session_id":"s1","rate_limits":{"seven_day":{"used_percentage":45,"resets_at":1720345600},"five_hour":{"used_percentage":30,"resets_at":1720003600}}}`},
		{
			`{"session_id":"s1","rate_limits":{"seven_day":{"used_percentage":45,"resets_at":1720345600},"five_hour":{"used_percentage":30,"resets_at":1720003600}}}`,
			`{"session_id":"s1","rate_limits":{"seven_day":{"used_percentage":45,"resets_at":1720345600},"five_hour":{"used_percentage":30,"resets_at":1720003600}}}`,
		},
		{"garbage{partial", `{"rate_limits":{"seven_day":{"used_percentage":"nan"}}}`},
		{"{}", `{"rate_limits":{"seven_day":{"used_percentage":1e400,"resets_at":1e400}}}`},
		{"", `{"rate_limits":{"seven_day":{"used_percentage":50},"five_hour":[]},"cost":{"total_cost_usd":12.5}}`},
		{"", `{"rate_limits":null}`},
		{"", `not-an-object`},
	}
	for _, s := range seeds {
		f.Add([]byte(s.pre), []byte(s.payload))
	}

	const now = int64(1720000000)
	f.Fuzz(func(t *testing.T, preJSON, payloadJSON []byte) {
		// Reset the DB each iteration so state does not leak across inputs.
		for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
			_ = os.Remove(p)
		}
		s, st := store.Open()
		if st != store.StateOK || s == nil {
			return
		}
		defer s.Close()
		appendRow := func(m map[string]any, now int64) {
			if row, ok := Prepare(s, m, now); ok { // Prepare must never panic on adversarial input
				_ = s.AppendHistory(row)
			}
		}
		if pre := rb.ParseObject(preJSON); pre != nil {
			appendRow(pre, now) // random pre-existing state
		}
		payload := rb.ParseObject(payloadJSON)
		if payload == nil {
			return // capture always hands Prepare a parsed object
		}
		appendRow(payload, now+1)
	})
}
