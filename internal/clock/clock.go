// Package clock is the one knob so every wall-clock TIME-OF-DAY in ccpool agrees (rhythm's busiest
// hour, status's reset time, check's clock line). Durations are unaffected. CCPOOL_CLOCK = 24
// (default) | 12 | auto; auto best-efforts the macOS preference and falls back to 24.
package clock

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/env"
)

// Mode resolves the clock to 12 or 24, read fresh from CCPOOL_CLOCK so per-call test env is honoured.
func Mode() int {
	switch strings.ToLower(strings.TrimSpace(env.String("CCPOOL_CLOCK", "24"))) {
	case "12":
		return 12
	case "auto":
		return detect()
	default: // "24", blank, or garbage: predictable default
		return 24
	}
}

// detect reads the macOS AppleICUForce24HourTime preference: "0" => 12h, else 24. Non-mac / absent
// / error => 24.
func detect() int {
	out, err := exec.Command("defaults", "read", "-g", "AppleICUForce24HourTime").Output()
	if err != nil {
		return 24
	}
	if strings.TrimSpace(string(out)) == "0" {
		return 12
	}
	return 24
}

// H12 reports whether the resolved mode is 12-hour.
func H12() bool { return Mode() == 12 }

// Hour renders an hour-of-day int (0..23) as "18:00" (24h) / "6pm" (12h).
func Hour(h int) string {
	if !H12() {
		return fmt.Sprintf("%02d:00", h)
	}
	switch {
	case h == 0:
		return "12am"
	case h == 12:
		return "12pm"
	case h < 12:
		return fmt.Sprintf("%dam", h)
	default:
		return fmt.Sprintf("%dpm", h-12)
	}
}

// Time renders a time as clock-time only, no date: "22:30" (24h) / "10:30pm" (12h).
func Time(t time.Time) string {
	if H12() {
		return t.Format("3:04pm") // Ruby %-I:%M%P
	}
	return t.Format("15:04") // Ruby %H:%M
}
