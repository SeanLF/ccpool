package statusline

import (
	"testing"

	"github.com/SeanLF/ccpool/internal/rb"
)

// FuzzRender feeds arbitrary JSON, decoded into the payload map via the shared rb.ParseObject, into
// the full Render + RenderCompact. These are the fail-open hot path (a panic escapes to Claude
// Code), so no input may panic. A nil store is passed so DPP() takes the cold-cache/fail-open branch
// (the exact state when store.Open fails on a render) rather than reading the dev's real DB.
func FuzzRender(f *testing.F) {
	f.Setenv("NO_COLOR", "1")

	seeds := []string{
		`{"rate_limits":{"seven_day":{"used_percentage":45,"resets_at":1720345600}}}`,
		`{"rate_limits":{"seven_day":{"used_percentage":95,"resets_at":1720345600}}}`,
		`{"context_window":{"used_percentage":63,"context_window_size":190000},"rate_limits":{"five_hour":{"used_percentage":82,"resets_at":1720003600},"seven_day":{"used_percentage":88,"resets_at":1720345600}}}`,
		`{"rate_limits":{"seven_day":{"used_percentage":45}}}`,
		`{"rate_limits":{"seven_day":{"used_percentage":"notanum"}}}`,
		`{"rate_limits":{"seven_day":{"used_percentage":1e400,"resets_at":-1e400}}}`,
		`{"rate_limits":{"five_hour":[],"seven_day":123}}`,
		`{"transcript_path":"/nope/does/not/exist","rate_limits":{}}`,
		`{}`, `{"rate_limits":null}`,
		`{"rate_limits":{"seven_day":{"used_percentage":50,"resets_at":9999999999999}}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	const now = int64(1720000000)
	f.Fuzz(func(t *testing.T, b []byte) {
		data := rb.ParseObject(b)
		if data == nil {
			return // not an object -> nothing to render
		}
		_ = Render(nil, data, now)
		_ = RenderCompact(nil, data, now)
	})
}
