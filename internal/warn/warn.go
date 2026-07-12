// Package warn is the situational-awareness hook (`ccpool warn`): it warns the agent mid-turn about
// things it can't see — weekly pace over the linear window share, a 5h session window nearing full,
// and the context window nearing auto-compaction. Two hook events:
//
//	UserPromptSubmit -> once per turn; emits plain stdout (added to context).
//	PostToolUse      -> after every tool; emits hookSpecificOutput.additionalContext, THROTTLED
//	                    per signal so it doesn't warn after every tool.
//
// Data source is the per-session snapshots the statusline writes. Fails open and SILENT throughout:
// missing/stale/garbled data or any error emits nothing, never a false alarm.
package warn

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	cfgfile "github.com/SeanLF/ccpool/internal/config" // aliased: this file already has a local `config` type
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/pool"
	"github.com/SeanLF/ccpool/internal/profile"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

// sig is one applicable warning: its throttle key, min gap between PostToolUse repeats, and text.
type sig struct {
	key      string
	throttle int64
	text     string
}

// config resolves the env-driven thresholds fresh (matching the Ruby module constants). Read per
// call; warn is cheap and this keeps per-fixture test env honoured.
type config struct {
	stale, throttle, ctxThrottle, coast int64
	ctxWarn, ctxWarnLeft, sesWarn       float64
	margin                              float64
	tmp                                 string
}

func load() config {
	return config{
		stale:       env.Int64("CCPOOL_WARN_STALE_SECS", 3600),
		throttle:    env.Int64("CCPOOL_WARN_THROTTLE_SECS", 1800),
		ctxThrottle: env.Int64("CCPOOL_WARN_CTX_THROTTLE_SECS", 600),
		coast:       env.Int64("CCPOOL_COAST_SECS", 43200),
		ctxWarn:     env.Float("CCPOOL_WARN_CTX_PCT", 85),
		ctxWarnLeft: float64(env.Int64("CCPOOL_WARN_CTX_LEFT", 30000)),
		sesWarn:     env.Float("CCPOOL_WARN_5H_PCT", 85),
		margin:      env.Float("CCPOOL_PACE_MARGIN", 3),
		tmp:         tmpDir(),
	}
}

// Hook is the `ccpool warn` command: read the hook payload from stdin, run, print the result (with
// a trailing newline, like Ruby's puts). FAILS OPEN and silent — a hook must NEVER break Claude Code.
func Hook(now int64) {
	defer func() { _ = recover() }()

	if !cfgfile.HooksEnabled() {
		return
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	payload := rb.ParseObject(raw)
	if payload == nil {
		payload = map[string]any{} // Ruby: a non-Hash payload becomes {}
	}
	if out := Run(payload, now); out != "" {
		fmt.Println(out)
	}
}

// Run is the entry point: hook payload (parsed) + now -> the string to print, or "". Never panics.
func Run(payload map[string]any, now int64) string {
	defer func() { _ = recover() }()

	// Open the store once for this hook invocation (fail open: a nil/non-OK store -> no snapshots ->
	// silent no-op, which is exactly warn's contract on missing data).
	s, _ := store.Open()
	if s != nil {
		defer s.Close()
	}
	snaps := pool.LoadSnapshots(s)
	if len(snaps) == 0 {
		return ""
	}
	event := "UserPromptSubmit"
	if e, ok := payload["hook_event_name"].(string); ok {
		event = e
	}
	sid, _ := payload["session_id"].(string)

	cfg := load()
	return emit(cfg, signals(cfg, snaps, sid, now), event, sid, now)
}

// signals is the pure decision: the warnings that apply right now. Side-effect-free (no markers, no
// stdin) so it is trivially testable.
func signals(cfg config, snaps []map[string]any, sessionID string, now int64) []sig {
	var out []sig

	if age, ok := pool.DataAge(snaps, now); ok && age <= cfg.stale {
		if wk, ok := pool.GetWindow(snaps, "seven_day", now, pool.Week+86400); ok {
			p := pool.GetPace(wk.Used, wk.Reset, now)
			if p.Delta > cfg.margin && p.ToReset > cfg.coast {
				out = append(out, sig{"pace", cfg.throttle, paceText(wk.Used, p)})
			}
		}
		if fh, ok := pool.GetWindow(snaps, "five_hour", now, 6*3600); ok && fh.Used >= cfg.sesWarn {
			out = append(out, sig{"session", cfg.throttle, sessionText(fh, now)})
		}
	}

	// CONTEXT is session-local: read THIS session's own snapshot, with its own freshness gate.
	if own := findSession(snaps, sessionID); own != nil {
		capturedAt, capOK := rb.Num(own["captured_at"])
		cw, cwOK := own["context_window"].(map[string]any)
		if capOK && now-int64(capturedAt) <= cfg.stale && cwOK {
			if ctx, ok := rb.Num(cw["used_percentage"]); ok && ctx >= 0 && ctx <= 100 && ctxNear(cfg, ctx, cw) {
				out = append(out, sig{"ctx", cfg.ctxThrottle, ctxText(ctx, cw)})
			}
		}
	}
	return out
}

// findSession returns this session's snapshot (nil if the id is empty or unmatched). Ruby treats an
// empty-string id as truthy and would search for a snapshot with id ""; we fold that pathological
// case (Claude Code always sends a UUID) into the absent case — no ctx signal. Fail-open either way.
func findSession(snaps []map[string]any, sessionID string) map[string]any {
	if sessionID == "" {
		return nil
	}
	for _, d := range snaps {
		if s, ok := d["session_id"].(string); ok && s == sessionID {
			return d
		}
	}
	return nil
}

// ctxLeft is the tokens of headroom before the window fills, or (0,false) when size is missing.
func ctxLeft(ctx float64, cw map[string]any) (float64, bool) {
	size, ok := rb.Num(cw["context_window_size"])
	if !ok || size <= 0 {
		return 0, false
	}
	return size * (100 - ctx) / 100.0, true
}

// ctxNear reports whether auto-compaction is near: prefer ABSOLUTE headroom (window-size-aware) so
// a 1M window isn't nagged at 85%; fall back to a flat % when the size field is missing.
func ctxNear(cfg config, ctx float64, cw map[string]any) bool {
	if left, ok := ctxLeft(ctx, cw); ok {
		return left <= cfg.ctxWarnLeft
	}
	return ctx >= cfg.ctxWarn
}

func paceText(used float64, p pool.Pace) string {
	against := "of the week elapsed"
	if profile.Load().Scheduled() {
		against = "of your work-rhythm pace"
	}
	return fmt.Sprintf(
		"[usage-pace] WEEKLY pace: %d%% used vs ~%d%% "+against+" (~%dpts ahead; resets in %s). "+
			"A PACE signal, NOT a stop order. If finishing the current task is cheaper than a clean handover, "+
			"push through -- a cold restart pays to rebuild context. If you stop, checkpoint properly: update "+
			"the relevant docs and leave a handover note so the next session resumes cheaply, don't just drop "+
			"it mid-task. Running unattended, aim for a comfortable checkpoint as you near the limit, not the "+
			"moment you cross pace. Run `ccpool check` before a big new push.",
		rb.RoundToInt(used), rb.RoundToInt(p.ElapsedPct), rb.RoundToInt(p.Delta), fmtx.Dur(p.ToReset),
	)
}

func sessionText(fh pool.Window, now int64) string {
	return fmt.Sprintf(
		"[usage-session] 5h SESSION window at %d%% (resets in %s) -- you will auto-throttle soon. Land or "+
			"checkpoint in-flight work before the pause. This is a short wait, not done for the week, if the "+
			"weekly pool still has room.",
		rb.RoundToInt(fh.Used), fmtx.Dur(fh.Reset-now),
	)
}

func ctxText(ctx float64, cw map[string]any) string {
	size := sizeStr(cw["context_window_size"])
	room := ""
	if left, ok := ctxLeft(ctx, cw); ok && left > 0 {
		room = " (~" + fmtx.Size(left) + " left)"
	}
	where := ""
	if size != "" {
		where = " of the " + size + " context window"
	}
	return fmt.Sprintf(
		"[context] this session is at %d%%%s%s -- auto-compaction is near. Land or checkpoint important "+
			"state now, and consider /compact at a clean point so it doesn't cut mid-task.",
		rb.RoundToInt(ctx), where, room,
	)
}

// emit applies per-signal throttling (PostToolUse only; UserPromptSubmit always emits) and formats
// for the firing event. Returns the string to print, or "" when nothing fires.
func emit(cfg config, sigs []sig, event, sessionID string, now int64) string {
	if len(sigs) == 0 {
		return ""
	}
	key := sanitizeKey(sessionID)

	var fire []sig
	for _, w := range sigs {
		if event == "PostToolUse" && now-readMarker(cfg, w, key) < w.throttle {
			continue
		}
		fire = append(fire, w)
	}
	for _, w := range fire {
		writeMarker(cfg, w, key, now)
	}
	if len(fire) == 0 {
		return ""
	}

	texts := make([]string, len(fire))
	for i, w := range fire {
		texts[i] = w.text
	}
	text := strings.Join(texts, "\n")

	if event != "PostToolUse" {
		return text
	}
	return hookJSON(text)
}

// hookJSON is the PostToolUse envelope, matching Ruby's JSON.generate (key order, no HTML escaping).
func hookJSON(text string) string {
	type inner struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	}
	payload := struct {
		HookSpecificOutput inner `json:"hookSpecificOutput"`
	}{inner{"PostToolUse", text}}

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return ""
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- markers, env, coercion ---

var keyUnsafe = regexp.MustCompile(`[^\w.-]`)

func sanitizeKey(sessionID string) string {
	if sessionID == "" {
		sessionID = "global"
	}
	return keyUnsafe.ReplaceAllString(sessionID, "")
}

func marker(cfg config, s sig, key string) string {
	return filepath.Join(cfg.tmp, "claude-"+s.key+"-"+key)
}

func readMarker(cfg config, s sig, key string) int64 {
	b, err := os.ReadFile(marker(cfg, s, key))
	if err != nil {
		return 0
	}
	return int64(rb.ToI(string(b)))
}

func writeMarker(cfg config, s sig, key string, now int64) {
	_ = os.WriteFile(marker(cfg, s, key), []byte(strconv.FormatInt(now, 10)), 0o644)
}

func tmpDir() string {
	if v := os.Getenv("TMPDIR"); v != "" {
		return v
	}
	return "/tmp"
}

func sizeStr(v any) string {
	if f, ok := rb.Num(v); ok {
		return fmtx.Size(f)
	}
	return ""
}
