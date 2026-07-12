// Package statusline renders the Claude Code statusLine from a fresh CC payload. The rate_limits %
// is account-global, so the payload IS current; the cached $/1% turns % into a dollar value. Output
// must stay byte-identical to the Ruby statusline.rb (ANSI included) — it is the conformance oracle.
//
// Groups by timescale:  now (context window + cache-TTL) · ses (5h) · wk (weekly meter + $ + day).
// ANSI is officially supported in statuslines (code.claude.com/docs/en/statusline).
package statusline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/calib"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/profile"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
	"github.com/muesli/termenv"
)

const (
	week      = 7 * 86400
	cacheTTL  = 3600 // statusline staleness display tiers (secs): fresh past this
	cacheWarn = 900  // ...dim/warn past this
	cacheCrit = 180  // ...and flag as critically stale past this
)

// eighths are the partial-cell glyphs (index 0..8); solid/track are the full and empty cells.
var eighths = [...]string{" ", "▏", "▎", "▍", "▌", "▋", "▊", "▉", "█"}

const (
	solid = "█"
	track = "░"
)

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

	// The bar is the default teal-cyan (downgraded to the active tier), unless CCPOOL_BAR_COLOR gives
	// an explicit raw-escape override; either way it is suppressed when colour is off (attr/seq both
	// return "" under Ascii).
	bar := os.Getenv("CCPOOL_BAR_COLOR")
	if bar == "" {
		bar = seq("#56B6C2")
	} else {
		bar = attr(bar)
	}

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

// Render builds the whole line from the CC payload. now is unix seconds. The store is threaded in for
// the (read-only, cache-only) $ lookup, so a render reads the calibration through the invocation's
// single open (nil store -> no $, fail open).
func Render(s *store.Store, data map[string]any, now int64) string {
	pal := loadPalette()
	prof := profile.Load()

	rl := typedHash(data, "rate_limits", "rate_limits")
	if rl == nil {
		rl = map[string]any{}
	}
	dppVal, hasDPP := calib.DPP(s)

	var nowGrp, sesGrp, wkGrp []string

	// context window %
	if cw := typedHash(data, "context_window", "context_window"); cw != nil {
		if ctx, ok := typedNum(cw, "used_percentage", "context_window.used_percentage"); ok {
			r := rb.RoundToInt(ctx)
			seg := "ctx " + sev(fmt.Sprintf("%d%%", r), r, 70, 90, pal)
			if s := fmtSize(cw["context_window_size"]); s != "" {
				seg += " " + s
			}
			nowGrp = append(nowGrp, seg)
		}
	}

	// prompt-cache countdown (only when near expiry)
	if path, ok := data["transcript_path"].(string); ok {
		if st := cacheState(path); st != nil {
			ttl := int64(cacheTTL)
			if st.ttl != nil {
				ttl = int64(*st.ttl)
			}
			left := st.ts + ttl - now
			switch {
			case left <= 0:
				nowGrp = append(nowGrp, "cache "+pal.bold+pal.red+"cold"+pal.reset)
			case left < cacheWarn:
				col := pal.yellow
				if left < cacheCrit {
					col = pal.bold + pal.red
				}
				nowGrp = append(nowGrp, "cache "+col+fmtDur(left)+pal.reset)
			}
		}
	}

	// 5h session
	if fh := typedHash(rl, "five_hour", "five_hour"); fh != nil {
		if used, ok := typedNum(fh, "used_percentage", "five_hour.used_percentage"); ok {
			s := rb.RoundToInt(used)
			seg := "ses " + sev(fmt.Sprintf("%d%%", s), s, 80, 92, pal)
			if reset, ok := typedNum(fh, "resets_at", "five_hour.resets_at"); ok {
				seg += " " + fmtDur(int64(reset)-now)
			}
			sesGrp = append(sesGrp, seg)
		}
	}

	// weekly meter + $ + day-share
	if sd := typedHash(rl, "seven_day", "seven_day"); sd != nil {
		if used, ok := typedNum(sd, "used_percentage", "seven_day.used_percentage"); ok {
			cols := env.Int("COLUMNS", 120)
			width := clampInt(cols-82, 14, 40)
			wr := rb.RoundToInt(used)
			wknum := sev(fmt.Sprintf("%d%%", wr), wr, 75, 90, pal)
			dollars := ""
			if hasDPP {
				left := (100 - used) * dppVal
				dollars = " " + pal.dim + fmtDollars(left) + pal.reset
			}
			if resetF, ok := typedNum(sd, "resets_at", "seven_day.resets_at"); ok {
				reset := int64(resetF)
				pace := prof.ElapsedFraction(reset-week, now, reset)
				daysLeft := float64(reset-now) / 86400.0
				if daysLeft < 0.0001 {
					daysLeft = 0.0001
				}
				day := clampFloat(min(100-used, (100-used)/daysLeft), 0, 100)
				wkGrp = append(wkGrp, "wk "+meter(used/100.0, &pace, width, pal)+" "+wknum+dollars+" "+
					fmtDur(reset-now)+" "+pal.dim+"day "+fmt.Sprintf("%d", rb.RoundToInt(day))+"%"+pal.reset)
			} else {
				wkGrp = append(wkGrp, "wk "+meter(used/100.0, nil, width, pal)+" "+wknum+dollars)
			}
		}
	}

	return joinGroups(pal, nowGrp, sesGrp, wkGrp)
}

// RenderCompact is the one-segment render for embedding in another statusline: ONLY ccpool's
// differentiator (pool $-left + pace), leaving ctx/5h/model/git to the host. "" when there's no
// weekly window to speak to.
func RenderCompact(s *store.Store, data map[string]any, now int64) string {
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

	r := rb.RoundToInt(used)
	parts := []string{"pool " + sev(fmt.Sprintf("%d%%", r), r, 75, 90, pal)}

	if dppVal, hasDPP := calib.DPP(s); hasDPP {
		left := (100 - used) * dppVal
		parts = append(parts, pal.dim+fmtDollars(left)+pal.reset)
	}

	// pace: over-pace (burning fast) is the red risk signal, under-pace is banked headroom (cyan).
	if resetF, ok := typedNum(sd, "resets_at", "seven_day.resets_at"); ok {
		reset := int64(resetF)
		d := rb.RoundToInt(used - prof.ElapsedFraction(reset-week, now, reset)*100)
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

func sev(text string, pct int, warn, crit int, pal palette) string {
	if pct >= crit {
		return pal.red + text + pal.reset
	}
	if pct >= warn {
		return pal.yellow + text + pal.reset
	}
	return text
}

func fmtDur(secs int64) string {
	if secs < 0 {
		secs = 0
	}
	d := secs / 86400
	r := secs % 86400
	h := r / 3600
	r %= 3600
	m := r / 60
	if d > 0 {
		return fmt.Sprintf("%dd%dh", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

// fmtSize renders a token count as "1M"/"200k"; "" unless the value is a positive JSON number.
func fmtSize(v any) string {
	if f, ok := rb.Num(v); ok {
		return fmtx.Size(f)
	}
	return ""
}

// fmtDollars is the $-left readout: "$1.2k" past a grand, else "$47".
func fmtDollars(n float64) string {
	if n >= 1000 {
		return "$" + rb.Fmt1(n/1000) + "k"
	}
	return fmt.Sprintf("$%d", rb.RoundToInt(n))
}

// meter is the coloured usage bar: on-pace fill cyan, over-pace tail red, partial leading edge in
// eighths, remaining dim. paceFrac nil => no pace overlay (pw defaults to 1.0, all cyan).
func meter(usedFrac float64, paceFrac *float64, width int, pal palette) string {
	usedW := usedFrac * float64(width)
	pw := 1.0
	if paceFrac != nil {
		pw = *paceFrac
	}
	paceW := pw * float64(width)
	var b strings.Builder
	for i := 0; i < width; i++ {
		fi := float64(i)
		switch {
		case fi+1 <= usedW:
			col := pal.bar
			if fi+0.5 >= paceW {
				col = pal.red
			}
			b.WriteString(col + solid + pal.reset)
		case fi < usedW:
			col := pal.bar
			if fi+0.5 >= paceW {
				col = pal.red
			}
			idx := rb.RoundToInt((usedW - fi) * 8)
			if idx < 1 {
				idx = 1
			}
			b.WriteString(col + eighths[idx] + pal.reset)
		default:
			b.WriteString(pal.dim + track + pal.reset)
		}
	}
	return b.String()
}

// joinGroups joins each non-empty group's items with two spaces, then the groups with SEP.
func joinGroups(pal palette, groups ...[]string) string {
	var joined []string
	for _, g := range groups {
		if len(g) > 0 {
			joined = append(joined, strings.Join(g, "  "))
		}
	}
	return strings.Join(joined, pal.sep)
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

// --- transcript cache state ---

type cacheInfo struct {
	ts  int64
	ttl *int // 300 (5m), 3600 (1h), or nil (unknown -> caller uses cacheTTL)
}

// cacheState reads the transcript tail for the last-activity epoch + live prompt-cache TTL.
// Returns nil on any problem (fail open). Not exercised by the conformance fixtures, but ported
// for real use.
func cacheState(path string) *cacheInfo {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	const window = 32768
	size := fi.Size()
	off := size - window
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, 0); err != nil {
		return nil
	}
	// Read to EOF from the seek point (Ruby f.read); io.ReadAll avoids the short-read/zero-fill
	// artifact a single Read can leave.
	tail, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	lines := splitLines(tail)
	// Drop a leading partial line when we seeked into the middle of the file.
	if size >= window && len(lines) > 1 {
		lines = lines[1:]
	}

	var entries []map[string]any
	for _, l := range lines {
		if m := rb.ParseObject(l); m != nil {
			entries = append(entries, m)
		}
	}
	if len(entries) == 0 {
		return nil
	}

	// last entry with a string timestamp
	var tsStr string
	found := false
	for i := len(entries) - 1; i >= 0; i-- {
		if s, ok := entries[i]["timestamp"].(string); ok {
			tsStr = s
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	ts, ok := parseTimestamp(tsStr)
	if !ok {
		return nil
	}

	var ttl *int
	for i := len(entries) - 1; i >= 0; i-- {
		cc := digObject(entries[i], "message", "usage", "cache_creation")
		if cc == nil {
			continue
		}
		if numToInt(cc["ephemeral_5m_input_tokens"]) > 0 {
			v := 300
			ttl = &v
			break
		}
		if numToInt(cc["ephemeral_1h_input_tokens"]) > 0 {
			v := 3600
			ttl = &v
			break
		}
	}
	return &cacheInfo{ts: ts, ttl: ttl}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	return out
}

// parseTimestamp parses an ISO8601 transcript timestamp to unix seconds (Ruby Time.parse().to_i).
func parseTimestamp(s string) (int64, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix(), true
		}
	}
	return 0, false
}

func digObject(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

// numToInt coerces a JSON value to int like Ruby `x.to_i` for the cache-token fields (nil -> 0).
func numToInt(v any) int {
	n, ok := v.(json.Number)
	if !ok {
		return 0
	}
	if i, err := n.Int64(); err == nil {
		return int(i)
	}
	if f, err := n.Float64(); err == nil {
		return int(f)
	}
	return 0
}

// --- small numeric helpers ---

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
