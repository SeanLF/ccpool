package analyzer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `ccpool review` is the corpus path that actually allocates: every line carrying "output_tokens"
// gets a full map[string]any decode (rb.ParseObject), i.e. thousands of small short-lived objects
// per file. (rhythm's scan, by contrast, never leaves []byte.) This benchmark isolates that decode
// so a toolchain change can be A/B'd rather than asserted -- Go 1.27 both moved encoding/json onto
// the v2 engine and specialized allocation of sub-80-byte objects, and both land right here.
//
// Read the number for what it is: ONE file, sequential, every line handed to the scan. The real
// command rejects most lines on the tokLit pre-filter before any parse, and fans the rest out
// across cores, so the decode is a minority of its wall time and a delta here lands about an order
// of magnitude smaller end-to-end (the 1.26->1.27 json v2 change: -38% here, -4% on `ccpool
// review`, and -4% even with the recency window opened wide enough to gate nothing away).
// Use this to detect a change; time the actual command before deciding the change matters.

// benchCorpus writes one transcript file of n lines in a realistic mix: expensive turns with and
// without tool calls, a cheap model, and user lines that the tokLit pre-filter rejects on raw bytes
// before any parse. Returns the path and its size in bytes.
func benchCorpus(tb testing.TB, dir string, n int, ts time.Time) (string, int64) {
	tb.Helper()
	stamp := ts.UTC().Format(time.RFC3339)
	var b strings.Builder
	for i := range n {
		switch i % 4 {
		case 0: // expensive, tool-using -> not trivial
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":"%s","message":{"id":"msg_%d","model":"claude-opus-5","usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d},"content":[{"type":"text","text":"reading the file"},{"type":"tool_use","id":"toolu_%d","name":"Bash","input":{"command":"ls -la","description":"list"}}]}}`+"\n",
				stamp, i, 1200+i, 900+i%400, 4096, 90000+i, i)
		case 1: // expensive, no tools, small output -> trivial
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":"%s","message":{"id":"msg_%d","model":"claude-opus-5","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d},"content":[{"type":"text","text":"ok"}]}}`+"\n",
				stamp, i, 1100+i, i%400, 88000+i)
		case 2: // cheap model -> counted, but not in the expensive tally
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":"%s","message":{"id":"msg_%d","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":%d,"output_tokens":%d},"content":[{"type":"text","text":"done"}]}}`+"\n",
				stamp, i, 300+i, 60+i%50)
		default: // user turn: no output_tokens, rejected by the byte pre-filter
			fmt.Fprintf(&b, `{"type":"user","timestamp":"%s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%d","content":"total 8\ndrwxr-xr-x  4 sean staff 128 Aug 20 12:00 ."}]}}`+"\n",
				stamp, i-1)
		}
	}
	path := filepath.Join(dir, "corpus.jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		tb.Fatalf("write corpus: %v", err)
	}
	// scanReviewFile is mtime-gated to the window; an old mtime would silently benchmark an early
	// return instead of the scan.
	if err := os.Chtimes(path, ts, ts); err != nil {
		tb.Fatalf("chtimes: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatalf("stat corpus: %v", err)
	}
	return path, info.Size()
}

// BenchmarkScanReviewFile times the per-file review scan: byte pre-filter, JSON decode, tally.
func BenchmarkScanReviewFile(b *testing.B) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	const lines = 20000
	path, size := benchCorpus(b, b.TempDir(), lines, now.Add(-time.Hour))
	cutoff := now.Unix() - 30*86400

	// Negative control: an mtime gate, a cutoff, or a pre-filter that rejected everything would make
	// this time an early return and report a flattering number. Assert the EXACT tally the generator
	// implies, not merely "non-zero" -- a partially scanned corpus is the failure mode that would
	// still look plausible.
	var probe fileAgg
	scanReviewFile(path, cutoff, 500, &probe)
	wantExp, wantTrivial := lines/2, lines/4 // i%4 in {0,1} are opus turns; i%4==1 is the trivial one
	if probe.expTurns != wantExp || probe.expTrivial != wantTrivial || len(probe.models) != 2 {
		b.Fatalf("corpus not fully scanned; benchmark would measure the wrong thing: %+v "+
			"(want expTurns=%d expTrivial=%d models=2)", probe, wantExp, wantTrivial)
	}

	b.SetBytes(size)
	b.ReportAllocs()
	for b.Loop() {
		var agg fileAgg
		scanReviewFile(path, cutoff, 500, &agg)
	}
}
