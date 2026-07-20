package statusline

import (
	"fmt"
	"strings"
	"testing"

	"github.com/SeanLF/ccpool/internal/rb"
)

// A seven_day window whose reset has already passed is stale (Anthropic reset it; the cached payload
// just hasn't caught up). Neither render should show the old weekly %, mirroring pool.GetWindow's drop.
func TestRenderSuppressesPastResetWeekly(t *testing.T) {
	now := int64(1_800_000_000)
	data := rb.ParseObject([]byte(fmt.Sprintf(
		`{"rate_limits":{"seven_day":{"used_percentage":88,"resets_at":%d}}}`, now-3600,
	)))
	if out := Render(nil, data, now); strings.Contains(out, "88%") {
		t.Fatalf("stale post-reset weekly should be suppressed in Render, got: %q", out)
	}
	if out := RenderCompact(nil, data, now); strings.Contains(out, "88%") {
		t.Fatalf("stale post-reset weekly should be suppressed in RenderCompact, got: %q", out)
	}
}

// A future reset renders normally -- the guard suppresses only past-reset, nothing else.
func TestRenderShowsLiveWeekly(t *testing.T) {
	now := int64(1_800_000_000)
	data := rb.ParseObject([]byte(fmt.Sprintf(
		`{"rate_limits":{"seven_day":{"used_percentage":88,"resets_at":%d}}}`, now+3600,
	)))
	if out := Render(nil, data, now); !strings.Contains(out, "88%") {
		t.Fatalf("live weekly should render, got: %q", out)
	}
}
