package statusline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/rb"
)

// A seven_day window whose reset has already passed is stale (Anthropic reset it; the cached payload
// just hasn't caught up). Neither render should show the old weekly %, mirroring pool.GetWindow's drop.
func TestRenderSuppressesPastResetWeekly(t *testing.T) {
	now := int64(1_800_000_000)
	data := rb.ParseObject([]byte(fmt.Sprintf(
		`{"rate_limits":{"seven_day":{"used_percentage":88,"resets_at":%d}}}`, now-3600,
	)))
	if out := Render(data, now); strings.Contains(out, "88%") {
		t.Fatalf("stale post-reset weekly should be suppressed in Render, got: %q", out)
	}
	if out := RenderCompact(data, now); strings.Contains(out, "88%") {
		t.Fatalf("stale post-reset weekly should be suppressed in RenderCompact, got: %q", out)
	}
}

// A future reset renders normally -- the guard suppresses only past-reset, nothing else.
func TestRenderShowsLiveWeekly(t *testing.T) {
	now := int64(1_800_000_000)
	data := rb.ParseObject([]byte(fmt.Sprintf(
		`{"rate_limits":{"seven_day":{"used_percentage":88,"resets_at":%d}}}`, now+3600,
	)))
	if out := Render(data, now); !strings.Contains(out, "88%") {
		t.Fatalf("live weekly should render, got: %q", out)
	}
}

// The week cells go red exactly when pool.GetPace (what status and warn read) says ahead by >=1pt,
// under every pace profile, so the line can't contradict the other surfaces. The weekly % carries no
// threshold colour of its own, so the whole 0..100 range is fair game.
func TestWeekCellsRedAgreesWithPoolPace(t *testing.T) {
	t.Setenv("TZ", "UTC")
	for _, k := range []string{"NO_COLOR", "TERM", "CCPOOL_COLOR", "CCPOOL_BAR_COLOR"} {
		t.Setenv(k, "") // an exported NO_COLOR would make red "" and every case vacuous
	}
	const reset = int64(1_720_345_600)
	red := loadPalette().red
	for _, prof := range []string{"", "weekdays", "workhours"} {
		t.Setenv("CCPOOL_PACE_PROFILE", prof)
		for now := reset - week + 1800; now < reset; now += 9*3600 + 1234 {
			for used := 0.0; used <= 100; used += 2.9 {
				data := rb.ParseObject([]byte(fmt.Sprintf(
					`{"rate_limits":{"seven_day":{"used_percentage":%g,"resets_at":%d}}}`, used, reset,
				)))
				// Red in the cells themselves (between the label and the %), not just the +N↑ cue.
				out := Render(data, now)
				cells := out[strings.Index(out, "wk"):strings.LastIndex(out, "%")]
				gotRed := strings.Contains(cells, red)
				wantRed := rb.RoundToInt(pool.GetPace(used, reset, now).Delta) >= 1
				if gotRed != wantRed {
					t.Fatalf("profile=%q used=%.1f now=%d: red cells=%v, pool ahead=%v", prof, used, now, gotRed, wantRed)
				}
			}
		}
	}
}
