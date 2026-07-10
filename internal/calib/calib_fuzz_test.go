package calib

import (
	"encoding/json"
	"strings"
	"testing"
)

// FuzzBlocksParse fuzzes the ccusage blocks parse path (the JSON decode + blocksArray extraction +
// per-block field reads that ccusageBlocks runs on ccusage's output). ccusage output is external
// and can be malformed; this parse feeds the $ calibration that the statusline reads, so a panic
// here would surface on the fail-open path. In-package so the unexported helpers are reachable
// without shelling out to a fake ccusage on every iteration.
func FuzzBlocksParse(f *testing.F) {
	seeds := []string{
		`{"blocks":[{"startTime":"2024-07-03T07:46:40Z","endTime":"2024-07-03T09:46:40Z","costUSD":150,"models":["claude-opus-4"],"isGap":false}]}`,
		`{"blocks":[]}`,
		`[]`,
		`[{"startTime":"bad","endTime":"also-bad","costUSD":"nope","models":[1,2,3],"isGap":true}]`,
		`{"other":[{"costUSD":1e400,"startTime":"2024-07-03T07:46:40Z","endTime":"2024-07-03T07:46:40Z"}]}`,
		`{"blocks":[{"models":[],"costUSD":5,"startTime":"2024-07-03T07:46:40+25:00","endTime":"2024-07-03T09:46:40Z"}]}`,
		`{"blocks":[null,42,"str",{"actualEndTime":"2024-07-03T09:46:40.123456789Z"}]}`,
		`{"blocks":{"not":"an-array"}}`,
		`not json at all`, ``, `{"blocks":`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	const now = int64(1720000000)
	f.Fuzz(func(t *testing.T, out []byte) {
		if strings.TrimSpace(string(out)) == "" {
			return
		}
		var doc any
		dec := json.NewDecoder(strings.NewReader(string(out)))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return
		}
		arr := blocksArray(doc)
		if arr == nil {
			return
		}
		// Replicate ccusageBlocks' per-block field reads (the parse path under test).
		var blocks []block
		for _, item := range arr {
			b, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if isGap, _ := b["isGap"].(bool); isGap {
				continue
			}
			if !allAnthropic(b["models"]) {
				continue
			}
			s, ok1 := parseTime(str(b["startTime"]))
			endStr := str(b["actualEndTime"])
			if endStr == "" {
				endStr = str(b["endTime"])
			}
			e, ok2 := parseTime(endStr)
			c, ok3 := numField(b, "costUSD")
			if !ok1 || !ok2 || !ok3 || e <= s {
				continue
			}
			blocks = append(blocks, block{s: s, e: e, cost: c})
		}
		// And the consumer that reads them, over a couple of arbitrary windows.
		_ = costBetween(blocks, now-week, now)
		_ = costBetween(blocks, now, now-week)
	})
}

const week = 7 * 86400
