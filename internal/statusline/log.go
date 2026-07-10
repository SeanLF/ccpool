package statusline

import (
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/paths"
)

var wsRun = regexp.MustCompile(`\s*\n\s*`)

// logAnomaly appends a one-line entry to the capped (~200 line) anomaly log. The happy path writes
// NOTHING — only a CC payload-schema change lands here, so a silently dropped segment still leaves
// a trail (`tail -f ~/.claude/statusline.log`). Best-effort: never returns an error, never panics.
func logAnomaly(level, msg string) {
	defer func() { _ = recover() }()

	msg = wsRun.ReplaceAllString(msg, " ")
	if len(msg) > 500 {
		msg = msg[:500]
	}
	path := paths.StatuslineLog()

	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		lines = splitKeepingNonEmpty(string(b))
	}
	entry := time.Now().Format("2006-01-02 15:04:05") + " [" + level + "] " + msg
	lines = append(lines, entry)
	if len(lines) > 200 {
		lines = lines[len(lines)-200:]
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// splitKeepingNonEmpty splits stored log content back into lines, mirroring Ruby readlines (which
// keeps each line's content; the trailing newline is re-added on write).
func splitKeepingNonEmpty(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
