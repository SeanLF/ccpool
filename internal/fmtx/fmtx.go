// Package fmtx holds the display formatters shared across commands (statusline, warn, and the
// on-demand readouts): token-size abbreviation and the coarse duration phrasing. Kept together so
// every command prints the same shapes.
package fmtx

import (
	"fmt"

	"github.com/SeanLF/ccpool/internal/rb"
)

// Size abbreviates a token count as "1M" / "200k"; "" for a non-positive value (Ruby fmt_size).
func Size(n float64) string {
	if n <= 0 {
		return ""
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%dM", rb.RoundToInt(n/1_000_000.0))
	}
	return fmt.Sprintf("%dk", rb.RoundToInt(n/1000.0))
}

// Dur is the coarse countdown phrasing ("5d 10h" / "3h 20m" / "now"), matching CCPool.dur. Distinct
// from the statusline meter's tighter fmt_dur ("1h0m"); this one reads for prose and the preview.
func Dur(secs int64) string {
	if secs <= 0 {
		return "now"
	}
	d := secs / 86400
	r := secs % 86400
	if d > 0 {
		return fmt.Sprintf("%dd %dh", d, r/3600)
	}
	return fmt.Sprintf("%dh %dm", r/3600, (r%3600)/60)
}
