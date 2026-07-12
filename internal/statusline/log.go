package statusline

import (
	"log/slog"
	"os"
	"strings"

	"github.com/SeanLF/ccpool/internal/paths"
)

// maxLogLines caps the anomaly log so a frequently-rendering statusline hitting a PERSISTENT schema
// drift can't grow it unbounded.
const maxLogLines = 200

// diag is the statusline hot path's diagnostic logger: structured slog records appended to the
// capped anomaly log (~/.ccpool/statusline.log). The happy path writes NOTHING -- only a CC
// payload-schema change or a recovered panic lands here, so a silently dropped segment still leaves a
// greppable trail (`tail -f ~/.ccpool/statusline.log`, or `grep field=... ~/.ccpool/statusline.log`).
// Fail-open: the backing writer swallows every I/O error and never panics, so logging a diagnostic
// can never itself blank the line.
var diag = slog.New(slog.NewTextHandler(cappedLog{}, &slog.HandlerOptions{Level: slog.LevelInfo}))

// cappedLog is the fail-open io.Writer behind diag: it appends each formatted slog record to the
// anomaly log, keeping only the most recent maxLogLines. The path is resolved per write so the
// hermetic test env (CCPOOL_STATUSLINE_LOG) is honoured. slog serializes calls to Write, so the
// read-modify-write needs no extra lock (a detached warm-calib child appending raw ccusage stderr to
// the same file is a pre-existing cross-process interleave, harmless for a best-effort trail).
type cappedLog struct{}

func (cappedLog) Write(p []byte) (int, error) {
	defer func() { _ = recover() }() // recovered panic returns the zero (0, nil) -> fail-open

	path := paths.StatuslineLog()
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		lines = splitKeepingNonEmpty(string(b))
	}
	lines = append(lines, splitKeepingNonEmpty(string(p))...) // a record is one line, but stay robust
	if len(lines) > maxLogLines {
		lines = lines[len(lines)-maxLogLines:]
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	return len(p), nil
}

// splitKeepingNonEmpty splits stored log content back into lines (the trailing newline is re-added on
// write).
func splitKeepingNonEmpty(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
