package history

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SeanLF/ccpool/internal/rb"
)

// FuzzSeed fuzzes the history seed/dedup/parse path with a random payload map and a random
// pre-existing history file. Seed runs on every render (fail-open hot path via the statusline), so
// no input may panic: the doc contract says "any error is returned for the caller to log, never
// panics". CCPOOL_HISTORY points at a temp file rewritten each iteration.
func FuzzSeed(f *testing.F) {
	histPath := filepath.Join(f.TempDir(), "hist.jsonl")
	f.Setenv("CCPOOL_HISTORY", histPath)

	// Seeds: (existing-file-content, payload-JSON). The payload is decoded via rb.ParseObject to a
	// map, matching how the real caller hands Seed its parsed CC payload.
	type seed struct{ hist, payload string }
	seeds := []seed{
		{"", `{"session_id":"s1","rate_limits":{"seven_day":{"used_percentage":45,"resets_at":1720345600},"five_hour":{"used_percentage":30,"resets_at":1720003600}}}`},
		{
			`{"t":1719999970,"wk":45,"wk_reset":1720345600,"ses":30,"ses_reset":1720003600,"tier":"max_20x","cost":null,"session":"s1"}` + "\n",
			`{"session_id":"s1","rate_limits":{"seven_day":{"used_percentage":45,"resets_at":1720345600},"five_hour":{"used_percentage":30,"resets_at":1720003600}}}`,
		},
		{"garbage\n{\"partial\":\n", `{"rate_limits":{"seven_day":{"used_percentage":"nan"}}}`},
		{"{}\n", `{"rate_limits":{"seven_day":{"used_percentage":1e400,"resets_at":1e400}}}`},
		{"", `{"rate_limits":{"seven_day":{"used_percentage":50},"five_hour":[]},"cost":{"total_cost_usd":12.5}}`},
		{"\x00\xff\n", `{"rate_limits":null}`},
		{"", `not-an-object`},
	}
	for _, s := range seeds {
		f.Add([]byte(s.hist), []byte(s.payload))
	}

	const now = int64(1720000000)
	f.Fuzz(func(t *testing.T, hist, payloadJSON []byte) {
		payload := rb.ParseObject(payloadJSON)
		if payload == nil {
			return // Seed's caller always hands it a parsed object
		}
		if err := os.WriteFile(histPath, hist, 0o644); err != nil {
			t.Skip()
		}
		// Must never panic; an error return is fine (the contract).
		_ = Seed(payload, now)
	})
}
