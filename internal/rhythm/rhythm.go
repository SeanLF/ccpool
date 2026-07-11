// Package rhythm is `ccpool rhythm` — a read-only circadian diagnostic. It scans the Claude Code
// transcript corpus (~/.claude/projects/**/*.jsonl), builds an hour-of-day + weekday histogram in
// the CURRENT machine's LOCAL time over a recency window, and SUGGESTS a pace profile. It never
// auto-applies: high circular-resultant R -> a concrete wake window; low R -> honest `even`.
//
// Detection is self-obviating (a schedule only helps, and is only readable, when R is high), so the
// suggester stance is the honest one. A recency window (default 30d = the current TZ regime) sidesteps
// timezone-travel smear without any change-point detection. Fails OPEN: any unreadable file/line is
// skipped; no activity -> an honest "nothing to read".
package rhythm

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/clock"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/rb"
)

// ts pulls the ISO8601 UTC date + hour out of a transcript line without a full JSON parse (speed
// over thousands of files): year, month, day, hour.
var ts = regexp.MustCompile(`"timestamp":"(\d{4})-(\d\d)-(\d\d)T(\d\d):`)

// blocks is the sparkline ramp: index 0 = space, 1..8 = eighth-block glyphs.
var blocks = []rune(" ▁▂▃▄▅▆▇█")

// --- tunables (read fresh; mirror the Ruby module constants `|| default`) ---

// window is the recency window in days (= the current TZ regime).
func window() int {
	return env.Int("CCPOOL_RHYTHM_WINDOW", 30)
}

// rStrong is the R at/above which the rhythm is sharp enough to schedule to.
func rStrong() float64 {
	return env.Float("CCPOOL_RHYTHM_R", 0.5)
}

// peakFrac: a bucket is "active" at >= this fraction of the peak bucket.
func peakFrac() float64 {
	return env.Float("CCPOOL_RHYTHM_PEAK", 0.25)
}

// scanResult is the corpus histogram in the machine's LOCAL frame, restricted to the last window days.
type scanResult struct {
	hours [24]int
	wdays [7]int
	n     int
}

// scan walks the corpus -> local-frame hour/weekday histogram. Never panics: any unreadable file or
// line is skipped (fail open per-file, per-line).
func scan(now int64) (res scanResult) {
	defer func() { _ = recover() }() // belt-and-suspenders: a scan crash must not break the caller

	lt := time.Unix(now, 0).In(time.Local)
	_, offSec := lt.Zone()
	offH := floorDiv(offSec, 3600) // whole-hour offset (matches Profile's integral convention)
	today := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, time.UTC)
	win := window()

	root := paths.Projects()
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // a vanished/errored entry must not discard the whole scan
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir // Ruby glob skips dot-dirs by default
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		scanFile(path, offH, today, win, &res)
		return nil
	})
	return res
}

// scanFile folds one transcript file into res. Fails open on open/read errors.
func scanFile(path string, offH int, today time.Time, win int, res *scanResult) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // transcript lines can be large
	for sc.Scan() {
		m := ts.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		y, mo, dd, h := rb.ToI(m[1]), rb.ToI(m[2]), rb.ToI(m[3]), rb.ToI(m[4])
		roll := floorDiv(h+offH, 24) // offset may push local past midnight (+/-1 day)
		date := time.Date(y, time.Month(mo), dd, 0, 0, 0, 0, time.UTC).AddDate(0, 0, roll)
		diff := int(today.Sub(date) / (24 * time.Hour)) // UTC-date -> local-date age in days
		if diff < 0 || diff > win {
			continue
		}
		res.hours[floorMod(h+offH, 24)]++ // local hour-of-day
		res.wdays[int(date.Weekday())]++  // local weekday (Sun=0)
		res.n++
	}
}

// circ computes circular stats over the 24h clock: R (resultant length, 0=flat..1=one sharp peak)
// and n. R~1 => one sharp time-of-day; R~0 => spread evenly.
func circ(counts [24]int) (r float64, n int) {
	for _, c := range counts {
		n += c
	}
	if n == 0 {
		return 0, 0
	}
	var s, c float64
	for h, cnt := range counts {
		a := 2 * math.Pi * float64(h) / 24
		s += float64(cnt) * math.Sin(a)
		c += float64(cnt) * math.Cos(a)
	}
	return math.Sqrt(s*s+c*c) / float64(n), n
}

// active returns the indices of buckets at >= peakFrac of the peak bucket.
func active(counts []int) []int {
	mx := 0
	for _, c := range counts {
		if c > mx {
			mx = c
		}
	}
	if mx == 0 {
		return []int{}
	}
	thr := float64(mx) * peakFrac()
	var out []int
	for i, c := range counts {
		if float64(c) >= thr {
			out = append(out, i)
		}
	}
	return out
}

// wakeWindow finds the smallest CIRCULAR arc covering all active hours -> [h0, h1). It locates the
// largest empty gap on the clock; the wake window is everything else. Returns a wrapping window
// (h1 <= h0) when activity straddles midnight — the caller must reject that.
func wakeWindow(activeHours []int) (int, int) {
	if len(activeHours) == 0 || len(activeHours) >= 24 {
		return 0, 24
	}
	sorted := append([]int(nil), activeHours...)
	sort.Ints(sorted)
	bestGap, gapStart := -1, sorted[0]
	for i, a := range sorted {
		b := sorted[(i+1)%len(sorted)]
		gap := floorMod(b-a-1, 24) // empty hours strictly between a and the next active hour
		if gap > bestGap {
			bestGap, gapStart = gap, a
		}
	}
	h0 := floorMod(gapStart+bestGap+1, 24) // first active hour after the largest gap
	h1 := h0 + (24 - bestGap)              // window length = clock minus the gap
	if h1 > 24 {
		h1 -= 24 // keep h1 in (h0, 24]; a wrap yields h1 <= h0
	}
	return h0, h1
}

// fmtSet renders an int set compactly: contiguous run -> "a-b", single -> "a", else comma list.
func fmtSet(xs []int) string {
	s := append([]int(nil), xs...)
	sort.Ints(s)
	if len(s) == 1 {
		return fmt.Sprintf("%d", s[0])
	}
	if s[len(s)-1]-s[0] == len(s)-1 { // contiguous == first..last
		return fmt.Sprintf("%d-%d", s[0], s[len(s)-1])
	}
	parts := make([]string, len(s))
	for i, x := range s {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return strings.Join(parts, ",")
}

// compact abbreviates a message count: >= 1000 -> "Nk" (rounded), else the plain number.
func compact(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", rb.RoundToInt(float64(n)/1000.0))
	}
	return fmt.Sprintf("%d", n)
}

// spark renders the 24h histogram as an eighth-block sparkline.
func spark(counts [24]int) string {
	mx := 0
	for _, c := range counts {
		if c > mx {
			mx = c
		}
	}
	var b strings.Builder
	for _, c := range counts {
		idx := 0
		if mx != 0 {
			idx = rb.RoundToInt(float64(c) / float64(mx) * 8)
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}

// Detect is the R-gated profile DECISION (no formatting): low R -> `even`, no schedule. High R with
// a clean (non-midnight-straddling) window -> a concrete wake window, further classified by the
// active-day set: every day active -> `workhours` (hour-only restriction); active days exactly
// Mon-Fri -> `weekdays`; any other day subset -> `custom`. workDays/wakeHours are "" when not
// applicable to the chosen profile. Mirrors the exact thresholds/window logic Suggestion used to
// inline (see Suggestion below for the formatting that consumes this).
func Detect(r float64, hours [24]int, wdays [7]int) (profile, workDays, wakeHours string) {
	if r < rStrong() {
		return "even", "", ""
	}
	h0, h1 := wakeWindow(active(hours[:]))
	if h1 <= h0 { // straddles midnight -> unrepresentable as a clean day-window
		return "even", "", ""
	}
	wakeHours = fmt.Sprintf("%d-%d", h0, h1)
	days := active(wdays[:])
	if len(days) == 7 {
		return "workhours", "", wakeHours
	}
	return dayProfile(days), fmtSet(days), wakeHours
}

// dayProfile classifies a non-full active-day set: exactly Mon-Fri (wday 1-5) -> `weekdays`;
// anything else (weekend-only, a partial week, ...) -> `custom`.
func dayProfile(days []int) string {
	if len(days) != 5 {
		return "custom"
	}
	for _, d := range days {
		if d < 1 || d > 5 {
			return "custom"
		}
	}
	return "weekdays"
}

// Suggestion is the R-gated recommendation line: formats Detect's decision into the human display
// string. Low R -> `even`; high R -> a concrete window (+ work-days if there's a day pattern),
// unless the window straddles midnight (unrepresentable -> honest `even`). Re-checks rStrong/the
// window itself only to pick WHICH `even` message applies -- Detect already made the one decision
// that matters (schedule vs. no schedule); this is display-string selection, not re-deciding.
func Suggestion(r float64, hours [24]int, wdays [7]int) string {
	profile, workDays, wakeHours := Detect(r, hours, wdays)
	if profile == "even" {
		if r < rStrong() {
			return "CCPOOL_PACE_PROFILE=even   (R too low for a schedule to help)"
		}
		return "CCPOOL_PACE_PROFILE=even   (strong, but the rhythm straddles midnight -- no clean day-window)"
	}
	parts := []string{"CCPOOL_WAKE_HOURS=" + wakeHours}
	if workDays != "" {
		parts = append(parts, "CCPOOL_WORK_DAYS="+workDays)
	}
	return strings.Join(parts, " ") + "   (strong rhythm -- pace to it)"
}

// Histogram builds the (R, hour-of-day, weekday) triple Detect needs, for callers (e.g. config
// seeding) that want the profile decision without the full Report text. Mirrors the scan+circ steps
// Report itself runs. Fails open via scan's own recover: no activity -> r=0 (Detect -> even).
func Histogram(now int64) (r float64, hours [24]int, wdays [7]int) {
	d := scan(now)
	r, _ = circ(d.hours)
	return r, d.hours, d.wdays
}

// Report renders the `ccpool rhythm` output as one line per element. Never panics.
func Report(now int64) (lines []string) {
	defer func() {
		if recover() != nil && lines == nil {
			lines = []string{}
		}
	}()

	win := window()
	d := scan(now)
	if d.n == 0 {
		return []string{fmt.Sprintf("rhythm: no transcript activity in the last %dd -- nothing to read.", win)}
	}

	r, n := circ(d.hours)
	busiest := indexOfMax(d.hours)
	quietest := indexOfMin(d.hours)
	_, offSec := time.Unix(now, 0).In(time.Local).Zone()
	off := fmt.Sprintf("UTC%+d", floorDiv(offSec, 3600))

	// Lead with the plain read; keep R (circular resultant, 0=flat..1=one sharp peak) as a detail.
	headline := "weak/continuous rhythm -- your loops fill the clock"
	if r >= rStrong() {
		headline = "strong day/night rhythm"
	}

	return []string{
		fmt.Sprintf("rhythm (last %dd · %s messages · local %s)", win, compact(n), off),
		fmt.Sprintf("  %s  (R=%.2f)", headline, r),
		"  activity  " + spark(d.hours) + "  midnight -> midnight",
		fmt.Sprintf("  busiest %s · quietest %s · active ~%dh/day", clock.Hour(busiest), clock.Hour(quietest), len(active(d.hours[:]))),
		"  suggested: " + Suggestion(r, d.hours, d.wdays),
	}
}

// indexOfMax / indexOfMin mirror Ruby Array#index(max|min): the FIRST index holding the extremum.
func indexOfMax(counts [24]int) int {
	best := 0
	for i, c := range counts {
		if c > counts[best] {
			best = i
		}
	}
	return best
}

func indexOfMin(counts [24]int) int {
	best := 0
	for i, c := range counts {
		if c < counts[best] {
			best = i
		}
	}
	return best
}

// floorDiv / floorMod match Ruby's Integer#div and #% (floored, non-negative modulo), which the
// UTC->local hour/date arithmetic relies on.
func floorDiv(a, b int) int {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func floorMod(a, b int) int {
	m := a % b
	if m != 0 && ((m < 0) != (b < 0)) {
		m += b
	}
	return m
}
