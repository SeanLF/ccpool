package rhythm

import "testing"

// TestLineTS covers the byte-level timestamp parse that replaced the per-line regex: a clean line, a
// line whose real timestamp trails an embedded (non-ISO) "timestamp":" in pasted content, junk after
// the literal, and no literal at all. The embedded-first case is the regex-parity guarantee.
func TestLineTS(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		y      int
		h      int
		wantOK bool
	}{
		{"clean", `{"type":"user","timestamp":"2026-07-20T11:30:00Z"}`, 2026, 11, true},
		{"nested-first", `{"toolUseResult":{"timestamp":"pending"},"timestamp":"2026-07-20T09:05:00Z"}`, 2026, 9, true},
		{"non-digit", `{"timestamp":"20xx-07-20T11:30:00Z"}`, 0, 0, false},
		{"truncated", `{"timestamp":"2026-07-2`, 0, 0, false},
		{"absent", `{"type":"summary"}`, 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			y, _, _, h, ok := lineTS([]byte(c.line))
			if ok != c.wantOK || (ok && (y != c.y || h != c.h)) {
				t.Errorf("lineTS: got (y=%d,h=%d,ok=%v), want (y=%d,h=%d,ok=%v)", y, h, ok, c.y, c.h, c.wantOK)
			}
		})
	}
}

func TestDetectLowRIsEven(t *testing.T) {
	var hours [24]int
	var wdays [7]int
	profile, wd, wh := Detect(0.0, hours, wdays) // R=0 -> no schedule
	if profile != "even" || wd != "" || wh != "" {
		t.Errorf("low R: got (%q,%q,%q), want (even,,)", profile, wd, wh)
	}
}

// allDaysActive mirrors a real scan where every weekday carries some (roughly even) traffic.
func allDaysActive() [7]int { return [7]int{10, 10, 10, 10, 10, 10, 10} }

// TestDetectHighRWorkhours: strong R, every weekday active (no day pattern), a single sharp
// hour-of-day peak -> `workhours` (hour-only restriction; workDays not applicable). This is the
// concrete-window branch Suggestion has always had (CCPOOL_WAKE_HOURS with no CCPOOL_WORK_DAYS).
func TestDetectHighRWorkhours(t *testing.T) {
	var hours [24]int
	hours[12] = 100 // sole peak -> wakeWindow collapses to [12, 13)

	profile, wd, wh := Detect(1.0, hours, allDaysActive())
	if profile != "workhours" || wd != "" || wh != "12-13" {
		t.Errorf("high R, all days: got (%q,%q,%q), want (workhours,,12-13)", profile, wd, wh)
	}
}

// TestDetectHighRWeekdays: strong R, active days exactly Mon-Fri -> `weekdays`, workDays set. This
// is the other concrete-window branch (CCPOOL_WAKE_HOURS + CCPOOL_WORK_DAYS).
func TestDetectHighRWeekdays(t *testing.T) {
	var hours [24]int
	hours[12] = 100
	var wdays [7]int
	wdays[1], wdays[2], wdays[3], wdays[4], wdays[5] = 10, 10, 10, 10, 10 // Mon-Fri only

	profile, wd, wh := Detect(1.0, hours, wdays)
	if profile != "weekdays" || wd != "1-5" || wh != "12-13" {
		t.Errorf("high R, Mon-Fri: got (%q,%q,%q), want (weekdays,1-5,12-13)", profile, wd, wh)
	}
}

// TestDetectHighRCustom: strong R, an active-day set that isn't Mon-Fri (weekend-only here) ->
// `custom`, workDays set.
func TestDetectHighRCustom(t *testing.T) {
	var hours [24]int
	hours[12] = 100
	var wdays [7]int
	wdays[0], wdays[6] = 10, 10 // Sun + Sat only

	profile, wd, wh := Detect(1.0, hours, wdays)
	if profile != "custom" || wd != "0,6" || wh != "12-13" {
		t.Errorf("high R, weekend-only: got (%q,%q,%q), want (custom,0\\,6,12-13)", profile, wd, wh)
	}
}

// TestDetectHighRStraddlesMidnight: strong R, but the active hours wrap past midnight (here
// 22:00-02:00) -> wakeWindow returns a wrapping window (h1 <= h0), unrepresentable as a clean
// day-window -> honest `even`, same as low R.
func TestDetectHighRStraddlesMidnight(t *testing.T) {
	var hours [24]int
	for _, h := range []int{22, 23, 0, 1, 2} {
		hours[h] = 10
	}

	profile, wd, wh := Detect(1.0, hours, allDaysActive())
	if profile != "even" || wd != "" || wh != "" {
		t.Errorf("high R, straddles midnight: got (%q,%q,%q), want (even,,)", profile, wd, wh)
	}
}
