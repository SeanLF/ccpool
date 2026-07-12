package statusline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/SeanLF/ccpool/internal/calib"
	"github.com/SeanLF/ccpool/internal/config"
	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/history"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/rb"
	"github.com/SeanLF/ccpool/internal/store"
)

// Command is the `ccpool statusline` entry point: capture the payload snapshot + history row, kick
// off the (detached) calibration warm-up, then render. FAILS OPEN — a statusline must NEVER break
// Claude Code, so a top-level recover swallows any panic (the Go analog of the Ruby blanket rescue).
// now is unix seconds; embed selects the compact render for host-statusline embedding.
func Command(now int64, embed bool) {
	defer func() {
		if r := recover(); r != nil {
			diag.Error("statusline panic", "recovered", fmt.Sprint(r))
		}
	}()

	if !config.HooksEnabled() {
		return
	}

	// No CC payload on a terminal stdin (reading it would hang) -> preview from the newest snapshot.
	if isTTY(os.Stdin) {
		preview(now, embed)
		return
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return
	}
	data := rb.ParseObject(raw)
	if data == nil {
		return
	}

	// Each best-effort step is isolated (like Ruby's per-method rescue) so the render always prints
	// once the payload parses; a panic in one can't blank the line. capture writes the snapshot and
	// (when not deduped) the history row in one store transaction.
	bestEffort("capture", func() { capture(raw, data, now) })
	bestEffort("warm", func() { warm(now) })
	if os.Getenv("CCPOOL_PRUNE") == "1" { // opt-in only: deleting files is never silent-by-default
		bestEffort("prune", func() { PruneCaches(now) })
	}

	var line string
	if embed {
		line = RenderCompact(data, now)
	} else {
		line = Render(data, now)
	}
	if line != "" {
		fmt.Print(line)
	}
}

// WarmCalib is the internal `__warm-calib` subcommand: the detached background $/1% recompute the
// warm-up spawns. Fail-open: never propagates an error.
func WarmCalib(now int64) {
	defer func() { _ = recover() }()
	calib.DollarPerPct(now, false)
}

// bestEffort runs a hot-path side effect, swallowing and logging any panic so it can never blank
// the render or reach Claude Code (the Go analog of Ruby's per-method rescue).
func bestEffort(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			diag.Warn("hot-path stage panic", "stage", name, "recovered", fmt.Sprint(r))
		}
	}()
	fn()
}

// capture writes the per-session snapshot (raw payload + captured_at) to the store, and the paired
// history row in the SAME transaction when history.Prepare says to append (not deduped/throttled) --
// so a reader never sees a snapshot without its history row. Only when session_id is a string.
// Fail-open: any non-OK store or write error is swallowed except a genuine append failure, which is
// logged to the anomaly log (the hot path stays silent otherwise).
func capture(raw []byte, data map[string]any, now int64) {
	sid, ok := data["session_id"].(string)
	if !ok {
		return
	}
	body, ok := spliceCapturedAt(raw, now)
	if !ok {
		return
	}
	s, st := store.Open()
	if st != store.StateOK {
		return // fail-open: no capture this render
	}
	defer s.Close()

	if row, appendRow := history.Prepare(s, data, now); appendRow {
		if err := s.CaptureAndAppend(sid, now, body, row); err != nil {
			diag.Warn("capture+append failed", "err", err)
		}
		return
	}
	_ = s.PutSnapshot(sid, now, body) // snapshot-only render (no new history row)
}

// spliceCapturedAt compacts the payload JSON and appends "captured_at":<now> as the last key,
// reproducing Ruby's `JSON.generate(payload.merge("captured_at" => now))` for realistic payloads
// (which carry canonical number/string tokens; see docs/DECISIONS.md on snapshot fidelity).
func spliceCapturedAt(raw []byte, now int64) ([]byte, bool) {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return nil, false
	}
	b := buf.Bytes()
	if len(b) < 2 || b[len(b)-1] != '}' {
		return nil, false
	}
	tail := `,"captured_at":` + strconv.FormatInt(now, 10) + `}`
	if b[len(b)-2] == '{' { // empty object: no leading comma
		tail = `"captured_at":` + strconv.FormatInt(now, 10) + `}`
	}
	out := make([]byte, 0, len(b)+len(tail))
	out = append(out, b[:len(b)-1]...)
	out = append(out, tail...)
	return out, true
}

// warm kicks off a DETACHED background $/1% recompute when the calibration is stale, so a
// statusline-only user still gets a $ without a render ever blocking on ccusage. Throttled to one
// attempt / 5 min via a marker file. Fail-open throughout.
func warm(now int64) {
	defer func() { _ = recover() }()

	self, err := os.Executable()
	if err != nil || strings.HasSuffix(self, ".test") {
		return // don't fork from a test binary (the Ruby $PROGRAM_NAME guard's intent)
	}
	if !calib.Stale(now) {
		return
	}
	mark := paths.CalibCache() + ".warming"
	if fi, err := os.Stat(mark); err == nil && now-fi.ModTime().Unix() < 300 {
		return
	}
	_ = os.WriteFile(mark, []byte(strconv.FormatInt(now, 10)), 0o644)

	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer devnull.Close()
	// stderr -> the anomaly log (NOT /dev/null): a ccusage schema-drift "fail LOUD" warning must
	// leave a trace for a statusline-only user. ccusage's own stderr is already suppressed.
	logf, err := os.OpenFile(paths.StatuslineLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer logf.Close()

	cmd := exec.Command(self, "__warm-calib")
	cmd.Stdout = devnull
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach so it outlives the render
	_ = cmd.Start()                                      // don't Wait: fire-and-forget
}

// preview renders `ccpool statusline` in a terminal from the freshest snapshot the real statusLine
// captured, so you can see the line without Claude Code. The rate_limits % is account-global so an
// old snapshot's % is still current; only ctx/cache are session-local (hence the stderr caveat).
func preview(now int64, embed bool) {
	data, ok := NewestSnapshot()
	if !ok {
		fmt.Fprintln(os.Stderr, "ccpool: no statusline snapshot yet. Wire `ccpool statusline` as your Claude Code statusLine first (see README), then it self-populates.")
		return
	}
	age := now - SnapshotCapturedAt(data)
	var line string
	if embed {
		line = RenderCompact(data, now)
	} else {
		line = Render(data, now)
	}
	fmt.Fprintf(os.Stderr, "[preview from a %s-old snapshot -- ctx/cache may be stale; live values come from Claude Code]\n", fmtx.Dur(age))
	if line != "" {
		fmt.Println(line)
	}
}

// NewestSnapshot returns the freshest per-session snapshot (by captured_at) the statusline captured,
// read from the store. ok=false when there is none or the store is unreadable -- the preview is a
// terminal convenience, so it degrades to a "wire it up" note rather than surfacing store internals.
// Exported so initcmd's post-install preview reads the newest snapshot the same way (one source).
func NewestSnapshot() (map[string]any, bool) {
	snaps, _ := store.ReadSnapshots()
	var newest map[string]any
	newestAt := int64(-1)
	for _, d := range snaps {
		if at := SnapshotCapturedAt(d); at > newestAt {
			newest, newestAt = d, at
		}
	}
	return newest, newest != nil
}

// SnapshotCapturedAt reads a snapshot map's captured_at epoch. store.Snapshots always splices it (from
// the payload or the row), so the 0 fallback is unreachable on the store path -- it exists only so a
// hand-built map can't panic here. Exported alongside NewestSnapshot so initcmd's post-install preview
// computes the age identically.
func SnapshotCapturedAt(data map[string]any) int64 {
	if n, ok := data["captured_at"].(json.Number); ok {
		if i, err := n.Int64(); err == nil {
			return i
		}
	}
	return 0
}

// PruneCaches deletes stale per-session snapshots (and orphaned tmp files) older than the keep
// window. Opt-in (CCPOOL_PRUNE=1); returns the count removed.
func PruneCaches(now int64) int {
	keep := env.Int64("CCPOOL_CACHE_KEEP_SECS", 3600)
	removed := 0
	globs := []string{paths.SnapshotGlob(), paths.SnapshotGlob() + ".*.tmp"}
	for _, g := range globs {
		matches, _ := filepath.Glob(g)
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil {
				continue
			}
			if now-fi.ModTime().Unix() > keep {
				if os.Remove(m) == nil {
					removed++
				}
			}
		}
	}
	return removed
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
