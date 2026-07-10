// Package analyzer is the retrospective over/under-provisioning review (`ccpool review`): a
// first-in-class judgement of whether you used the RIGHT model for the work. Effort isn't logged
// per turn, so it proxies "complexity" from output-token volume + tool-call count (Anthropic's own
// signal: high effort ~= more output + more tool calls). It scans Claude Code transcripts under
// ~/.claude/projects/**/*.jsonl over a `days` window, builds a per-model turn/output breakdown, and
// flags expensive-model turns (opus/fable) doing trivial work (candidates to downshift). Heuristic
// by nature -- the rendered output discloses the caveats. This is an on-demand command: it does NOT
// need the fail-open contract of the hot path.
package analyzer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/SeanLF/ccpool/internal/env"
	"github.com/SeanLF/ccpool/internal/paths"
	"github.com/SeanLF/ccpool/internal/rb"
)

// ModelStat is one model's tally over the window: total turns and total output tokens.
type ModelStat struct {
	Model string
	Turns int
	Out   int
}

// Result mirrors the Ruby Analyzer.review return hash: the per-model breakdown (sorted by turns
// descending) plus the expensive-model triviality stats.
type Result struct {
	Days          int
	ByModel       []ModelStat
	ExpTurns      int
	ExpOut        int
	ExpTrivial    int
	ExpTrivialOut int
	TrivialPct    float64
	TrivialOutPct float64
}

// lowOut is the output-token threshold below which (with no tool calls) an expensive turn counts as
// trivial. Env-overridable (CCPOOL_LOW_OUTPUT, default 500), read fresh so the hermetic test env is
// honoured; unset OR unparseable -> the default.
func lowOut() int {
	return env.Int("CCPOOL_LOW_OUTPUT", 500)
}

// Review scans the transcript window and returns the per-model + triviality summary. Mirrors
// Ruby Analyzer.review(days:, now:) exactly.
func Review(days int, now int64) Result {
	cutoff := now - int64(days)*86400
	threshold := lowOut()

	stats := map[string]*ModelStat{}
	var order []string // first-seen model order, so the turns-desc sort is deterministic on ties
	res := Result{Days: days}

	for _, f := range jsonlFiles(paths.Projects()) {
		info, err := os.Stat(f)
		if err != nil || info.ModTime().Unix() < cutoff {
			continue
		}
		forEachLine(f, func(line []byte) {
			if !strings.Contains(string(line), "output_tokens") {
				return
			}
			j := rb.ParseObject(line)
			if j == nil || asString(j["type"]) != "assistant" {
				return
			}
			msg, ok := j["message"].(map[string]any)
			if !ok {
				return
			}
			m := asString(msg["model"])
			// Skip router/synthetic turns: only genuine Claude models count.
			if m == "" || m == "<synthetic>" || !containsFold(m, "claude") {
				return
			}
			u, ok := msg["usage"]
			if !ok || u == nil {
				return
			}
			ts := parseTS(asString(j["timestamp"]))
			if ts < cutoff {
				return
			}

			out := 0
			if um, ok := u.(map[string]any); ok {
				out = toI(um["output_tokens"])
			}
			tools := countToolUse(msg["content"])

			s := stats[m]
			if s == nil {
				s = &ModelStat{Model: m}
				stats[m] = s
				order = append(order, m)
			}
			s.Turns++
			s.Out += out

			if !containsFold(m, "opus") && !containsFold(m, "fable") {
				return
			}
			res.ExpTurns++
			res.ExpOut += out
			if out < threshold && tools == 0 {
				res.ExpTrivial++
				res.ExpTrivialOut += out
			}
		})
	}

	res.ByModel = sortByTurns(stats, order)
	if res.ExpTurns > 0 {
		res.TrivialPct = 100.0 * float64(res.ExpTrivial) / float64(res.ExpTurns)
	}
	if res.ExpOut > 0 {
		res.TrivialOutPct = 100.0 * float64(res.ExpTrivialOut) / float64(res.ExpOut)
	}
	return res
}

// sortByTurns orders the tally by turns descending. Ruby's sort_by is unstable, but keeping
// first-seen order for ties makes the Go output deterministic; conformance fixtures avoid ties.
func sortByTurns(stats map[string]*ModelStat, order []string) []ModelStat {
	out := make([]ModelStat, 0, len(order))
	for _, m := range order {
		out = append(out, *stats[m])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Turns > out[j].Turns })
	return out
}

// RenderCommand builds the exact `ccpool review` output for the given args (args[0] optionally the
// days count) as a string ending in a newline, mirroring Ruby CCPool.review's `puts` sequence.
func RenderCommand(args []string, now int64) string {
	days := 7
	if len(args) > 0 {
		if d := rb.ToI(args[0]); d > 0 {
			days = d
		}
	}
	return render(Review(days, now))
}

// ReviewCommand prints the review readout to stdout (on-demand command; fail-loud, no recover).
func ReviewCommand(args []string, now int64) {
	fmt.Print(RenderCommand(args, now))
}

func render(r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Model provisioning review -- last %dd\n", r.Days)
	if len(r.ByModel) == 0 {
		b.WriteString("  no Claude turns found in the window.\n")
		return b.String()
	}
	n := len(r.ByModel)
	if n > 6 {
		n = 6
	}
	for _, m := range r.ByModel[:n] {
		fmt.Fprintf(&b, "  %6d turns  %6dk out  %s\n", m.Turns, m.Out/1000, m.Model)
	}
	if r.ExpTurns > 0 {
		b.WriteString("\n")
		fmt.Fprintf(&b, "  Expensive-model turns (opus/fable): %d\n", r.ExpTurns)
		fmt.Fprintf(&b, "  ...low-complexity (little output, no tools): %d (%d%%) -- candidates to downshift to sonnet/haiku\n",
			r.ExpTrivial, rb.RoundToInt(r.TrivialPct))
		fmt.Fprintf(&b, "  ~%d%% of your expensive-model output tokens went to that trivial work.\n",
			rb.RoundToInt(r.TrivialOutPct))
	}
	b.WriteString("\n")
	b.WriteString("  caveat: effort isn't logged per-turn -- this proxies complexity from output volume +\n")
	b.WriteString("  tool-calls; `ultrathink`/thinking inflate output invisibly, so treat as a hint, not a verdict.\n")
	return b.String()
}

// --- transcript scanning helpers ---

// jsonlFiles returns every *.jsonl under base (recursive), mirroring Dir.glob("base/**/*.jsonl").
// A missing base or any walk error yields the files found so far; aggregation is order-independent.
func jsonlFiles(base string) []string {
	var out []string
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(path, ".jsonl") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// forEachLine invokes fn with each line of the file (newline trimmed). Best-effort: an unreadable
// file is skipped. Uses a large buffer because transcript lines can be big.
func forEachLine(path string, fn func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<26)
	for sc.Scan() {
		fn(sc.Bytes())
	}
}

// countToolUse counts content entries that are objects with "type":"tool_use" (Ruby's tool count).
func countToolUse(v any) int {
	arr, ok := v.([]any)
	if !ok {
		return 0
	}
	n := 0
	for _, c := range arr {
		if cm, ok := c.(map[string]any); ok && asString(cm["type"]) == "tool_use" {
			n++
		}
	}
	return n
}

// parseTS mirrors `Time.parse(ts).to_i rescue 0`: unix seconds of an RFC3339 timestamp, else 0.
func parseTS(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return 0
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func containsFold(s, sub string) bool { return strings.Contains(strings.ToLower(s), sub) }

// toI mirrors Ruby #to_i on a JSON value: an integer literal is exact, a float truncates toward
// zero, a numeric string parses its prefix, everything else is 0.
func toI(v any) int {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return int(i)
		}
		if f, err := x.Float64(); err == nil {
			return int(f)
		}
		return 0
	case string:
		return rb.ToI(x)
	default:
		return 0
	}
}
