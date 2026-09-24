// Package statusline renders the Claude Code statusLine from a fresh CC payload. The rate_limits %
// is account-global, so the payload IS current. No $ here (see Render); `status` has it. Output
// must stay byte-identical to the committed goldens (conformance/golden/, ANSI included).
//
// Groups by timescale:  now (context window + cache-TTL) · 5h · wk (7 day cells + % + reset).
// ANSI is officially supported in statuslines (code.claude.com/docs/en/statusline).
package statusline

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/profile"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/muesli/termenv"
)

const (
	week      = 7 * 86400
	cacheWarn = 900 // prompt-cache seconds left: yellow (not dim) under this
	cacheCrit = 180 // ...and bold red under this
)

// heights are the partial-height cell glyphs (index 0..8, empty to full).
var heights = [...]string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// future is an empty day cell: dim, and a dot rather than a grey background, which reads as a heavy
// block on light terminal themes (and a non-tty statusline can't detect the theme).
const future = "·"

// palette holds the ANSI escapes, each already gated on the colour decision (empty when colour is
// off). Resolved per render so per-call NO_COLOR/TERM/CCPOOL_BAR_COLOR env is honoured.
type palette struct {
	reset, dim, yellow, red, bar, bold, sep string
}

func loadPalette() palette {
	prof := colorProfile()

	// seq is a foreground colour as an SGR prefix, downgraded to the active profile (e.g. 24-bit
	// "38;2;.." -> 256-colour "38;5;.." -> 16-colour "36" -> "" for Ascii). termenv does the matching.
	seq := func(spec string) string {
		s := prof.Color(spec).Sequence(false)
		if s == "" {
			return ""
		}
		return "\x1b[" + s + "m"
	}
	// attr gates a non-colour SGR attribute (reset/dim/bold) on colour being on at all.
	attr := func(code string) string {
		if prof == termenv.Ascii {
			return ""
		}
		return code
	}

	// The bar is dim like the rest of the line while on pace, so red past pace is the only colour
	// that means "look". CCPOOL_BAR_COLOR gives an explicit raw-escape override; either way it is
	// suppressed when colour is off (attr returns "" under Ascii).
	bar := os.Getenv("CCPOOL_BAR_COLOR")
	if bar == "" {
		bar = "\x1b[2m"
	}
	bar = attr(bar)

	p := palette{
		reset:  attr("\x1b[0m"),
		dim:    attr("\x1b[2m"),
		yellow: seq("11"), // bright yellow; already 16-colour, so unchanged across the colour tiers
		red:    seq("9"),  // bright red
		bar:    bar,
		bold:   attr("\x1b[1m"),
	}
	p.sep = " " + p.dim + "·" + p.reset + " "
	return p
}

// colorProfile resolves how much colour to emit. Claude Code invokes the statusLine with a NON-TTY
// stdout yet renders ANSI, so termenv's own auto-detection would strip everything; we force TrueColor
// by default and let CCPOOL_COLOR opt down a tier (a non-tty hook can't auto-detect the real
// terminal). NO_COLOR / TERM=dumb still win: forcing the profile bypasses termenv's NO_COLOR check,
// so it is gated manually. An unknown CCPOOL_COLOR fails open to TrueColor.
func colorProfile() termenv.Profile {
	if noColorEnv() {
		return termenv.Ascii
	}
	switch strings.ToLower(strings.TrimSpace(env.String("CCPOOL_COLOR", ""))) {
	case "256", "8bit":
		return termenv.ANSI256
	case "16", "ansi":
		return termenv.ANSI
	case "ascii", "none", "off":
		return termenv.Ascii
	case "auto":
		return termenv.NewOutput(os.Stdout).Profile // termenv's own detection (strips on a non-tty pipe)
	default: // "", "truecolor", "24bit", or anything unrecognised
		return termenv.TrueColor
	}
}

// noColorEnv preserves the pre-termenv contract exactly: NO_COLOR present AND non-empty, or TERM=dumb.
// (termenv.EnvNoColor also honours CLICOLOR and treats an empty NO_COLOR as set, which would differ.)
func noColorEnv() bool {
	v, ok := os.LookupEnv("NO_COLOR")
	return (ok && v != "") || os.Getenv("TERM") == "dumb"
}

// Render builds the whole line from the CC payload. now is unix seconds. No $ (here or in
// RenderCompact): a glance should answer "am I ahead of pace", and cold reads took "$1.4k" for money
// spent; `status` has it.
func Render(data map[string]any, now int64) string {
	pal := loadPalette()
	prof := profile.Load()

	rl := typedHash(data, "rate_limits", "rate_limits")
	if rl == nil {
		rl = map[string]any{}
	}
	var nowGrp, sesGrp, wkGrp []string

	// context window %
	if cw := typedHash(data, "context_window", "context_window"); cw != nil {
		if ctx, ok := typedNum(cw, "used_percentage", "context_window.used_percentage"); ok {
			r := rb.RoundToInt(ctx)
			seg := pal.quiet("ctx") + " " + sev(pct(r), r, 70, 90, pal)
			if s := fmtSize(cw["context_window_size"]); s != "" {
				seg += " " + pal.dim + s + pal.reset
			}
			nowGrp = append(nowGrp, seg)
		}
	}

	// prompt-cache countdown from CC's own prompt_cache (v2.1.251+): always shown so the line doesn't
	// shift, dim until it's near expiry, where colour becomes the alert. CC re-renders at expires_at,
	// so the flip to cold lands on time without a refresh tick.
	if pc := typedHash(data, "prompt_cache", "prompt_cache"); pc != nil {
		if seen, _ := pc["caching_observed"].(bool); seen {
			warm, _ := pc["warm"].(bool)
			exp, hasExp := typedNum(pc, "expires_at", "prompt_cache.expires_at")
			left := int64(exp) - now
			switch {
			case warm && !hasExp:
				// warm but no expiry to count down: say nothing rather than a false "cold"
			case !warm || left <= 0:
				nowGrp = append(nowGrp, pal.quiet("cache")+" "+pal.bold+pal.red+"cold"+pal.reset)
			case left < cacheWarn:
				col := pal.yellow
				if left < cacheCrit {
					col = pal.bold + pal.red
				}
				nowGrp = append(nowGrp, pal.quiet("cache")+" "+col+cacheLeft(left)+pal.reset)
			default:
				nowGrp = append(nowGrp, pal.quiet("cache "+cacheLeft(left)))
			}
		}
	}

	// 5h session
	if fh := typedHash(rl, "five_hour", "five_hour"); fh != nil {
		if used, ok := typedNum(fh, "used_percentage", "five_hour.used_percentage"); ok {
			s := rb.RoundToInt(used)
			seg := pal.quiet("5h-ses") + " " + sev(pct(s), s, 80, 92, pal)
			if reset, ok := typedNum(fh, "resets_at", "five_hour.resets_at"); ok {
				seg += " " + pal.quiet(resetIn(int64(reset)-now))
			}
			sesGrp = append(sesGrp, seg)
		}
	}

	// weekly: 7 day cells + % + reset
	if sd := typedHash(rl, "seven_day", "seven_day"); sd != nil {
		used, hasUsed := typedNum(sd, "used_percentage", "seven_day.used_percentage")
		resetF, hasReset := typedNum(sd, "resets_at", "seven_day.resets_at")
		// Past-reset guard: a stale payload whose window already reset must not show its old % (mirrors
		// pool.GetWindow dropping reset<=now); suppress the whole weekly segment until the payload catches up.
		if hasUsed && !(hasReset && int64(resetF) <= now) {
			// No %-threshold colour here: pace is the weekly alarm, so red means one thing (97% used an
			// hour before reset is fine). The +N↑ repeats the red cells in text, so the signal survives
			// colour-blindness and NO_COLOR.
			// The label, % and on-pace cells stay quiet; only ahead-of-pace lights up.
			wknum := pct(rb.RoundToInt(used))
			if hasReset {
				reset := int64(resetF)
				pace := prof.ElapsedFraction(reset-week, now, reset)
				d := paceDelta(used, pace)
				seg := pal.quiet("wk") + " " + days(used/100.0, pace, d >= 1, pal) + " "
				if d >= 1 {
					seg += wknum + " " + pal.red + fmt.Sprintf("+%d↑", d) + pal.reset
				} else {
					seg += pal.quiet(wknum)
				}
				wkGrp = append(wkGrp, seg+" "+pal.quiet(resetIn(reset-now)))
			} else {
				wkGrp = append(wkGrp, pal.quiet("wk")+" "+days(used/100.0, 1.0, false, pal)+" "+pal.quiet(wknum))
			}
		}
	}

	return joinGroups(pal, nowGrp, sesGrp, wkGrp)
}

// RenderCompact is the one-segment render for embedding in another statusline: ONLY ccpool's
// differentiator (weekly % + pace), leaving ctx/5h/model/git to the host. "" when there's no weekly
// window to speak to.
func RenderCompact(data map[string]any, now int64) string {
	pal := loadPalette()
	prof := profile.Load()

	rl := typedHash(data, "rate_limits", "rate_limits")
	if rl == nil {
		rl = map[string]any{}
	}
	sd := typedHash(rl, "seven_day", "seven_day")
	if sd == nil {
		return ""
	}
	used, ok := typedNum(sd, "used_percentage", "seven_day.used_percentage")
	if !ok {
		return ""
	}
	// Past-reset guard: a stale payload whose window already reset shows no weekly (mirrors pool).
	if resetF, ok := typedNum(sd, "resets_at", "seven_day.resets_at"); ok && int64(resetF) <= now {
		return ""
	}

	r := rb.RoundToInt(used)
	parts := []string{fmt.Sprintf("pool %d%%", r)} // uncoloured: the pace arrow is the alarm, as in Render

	// pace: over-pace (burning fast) is the red risk signal, under-pace is banked headroom (dim).
	if resetF, ok := typedNum(sd, "resets_at", "seven_day.resets_at"); ok {
		reset := int64(resetF)
		d := paceDelta(used, prof.ElapsedFraction(reset-week, now, reset))
		if d >= 1 || d <= -1 {
			if d > 0 {
				parts = append(parts, fmt.Sprintf("%s+%d↑%s", pal.red, d, pal.reset))
			} else {
				parts = append(parts, fmt.Sprintf("%s%d↓%s", pal.bar, d, pal.reset))
			}
		}
	}

	return strings.Join(parts, " ")
}

// --- rendering helpers ---

// sev is a value's severity: dim while healthy, yellow near its limit, red at it. The line is
// ambient by default, so anything not dim means "look at me".
func sev(text string, pct int, warn, crit int, pal palette) string {
	if pct >= crit {
		return pal.red + text + pal.reset
	}
	if pct >= warn {
		return pal.yellow + text + pal.reset
	}
	return pal.quiet(text)
}

// quiet dims text that should sit in the background: labels, countdowns, healthy values.
func (p palette) quiet(text string) string { return p.dim + text + p.reset }

// Fixed-width fields, like tabular figures, so a value changing width (9% -> 10%, 58m -> 9m)
// doesn't shift every segment after it. Durations zero-pad their inner unit for the same reason,
// which keeps widths steady without visible double spaces.
func pct(n int) string { return fmt.Sprintf("%2d%%", n) }

// resetIn is "↻" plus exactly 5 runes for any real window (up to 7d): "0h15m", "9h59m", then the
// day form from 10h ("0d23h", "6d23h"), since "23h59m" would be 6. Minutes only matter under 10h,
// which is the whole 5h window.
func resetIn(secs int64) string {
	secs = max(secs, 0)
	switch {
	case secs < 36000:
		return fmt.Sprintf("↻%dh%02dm", secs/3600, secs%3600/60)
	default:
		return fmt.Sprintf("↻%dd%02dh", secs/86400, secs%86400/3600)
	}
}

// cacheLeft caps at 59m: the TTL is at most an hour, so only the first second after a write would
// read "1h00m" and shift everything after it by two columns.
func cacheLeft(secs int64) string { return fmt.Sprintf("%02dm left", min(max(secs, 0), 3599)/60) }

// fmtSize renders a token count as "1M"/"200k"; "" unless the value is a positive JSON number.
func fmtSize(v any) string {
	if f, ok := rb.Num(v); ok {
		return fmtx.Size(f)
	}
	return ""
}

// paceDelta is used% minus the pace-profile elapsed%, rounded: the same number pool.GetPace (status,
// warn) reports, so every surface agrees on "ahead" (>= 1).
func paceDelta(used, paceFrac float64) int {
	return rb.RoundToInt(used - paceFrac*100)
}

// days is the week as 7 cells, each a seventh of it, filled in order by cumulative use. When ahead
// of pace (paceDelta >= 1), the cells holding use past the pace point are red.
// At most one colour per cell plus empty space, so a cell never needs a background colour.
func days(usedFrac, paceFrac float64, ahead bool, pal palette) string {
	var b strings.Builder
	for k := range 7 {
		f := min(max(usedFrac*7-float64(k), 0), 1)
		n := rb.RoundToInt(f * 8)
		if n == 0 && f > 1e-9 {
			n = 1 // any use shows, however small
		}
		if n == 0 {
			b.WriteString(pal.dim + future + pal.reset)
			continue
		}
		col := pal.bar
		if ahead && float64(k+1)/7 > paceFrac {
			col = pal.red
		}
		b.WriteString(col + heights[n] + pal.reset)
	}
	return b.String()
}

// joinGroups joins every segment with SEP. The groups (now, 5h, week) only order the segments; a
// wider gap inside a group read as an uneven space rather than a grouping.
func joinGroups(pal palette, groups ...[]string) string {
	var all []string
	for _, g := range groups {
		all = append(all, g...)
	}
	return strings.Join(all, pal.sep)
}

// --- typed payload access (mirrors statusline.rb typed?) ---

// typedHash returns m[key] as an object, or nil if absent/null/wrong-typed. A present-but-wrong
// value logs an anomaly (the signal a CC schema change silently dropped a segment).
func typedHash(m map[string]any, key, label string) map[string]any {
	v, present := m[key]
	if v == nil {
		return nil // absent or JSON null: silent + expected
	}
	if h, ok := v.(map[string]any); ok {
		return h
	}
	if present {
		diag.Warn("segment not an object", "field", label, "got", fmt.Sprintf("%T", v))
	}
	return nil
}

// typedNum returns m[key] as a float64 if it is a JSON number, else (0,false). Wrong-typed logs.
func typedNum(m map[string]any, key, label string) (float64, bool) {
	v, present := m[key]
	if v == nil {
		return 0, false
	}
	if n, ok := v.(json.Number); ok {
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	if present {
		diag.Warn("segment not a number", "field", label, "got", fmt.Sprintf("%T", v))
	}
	return 0, false
}
