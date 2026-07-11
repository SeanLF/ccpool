package statusline

import (
	"os"
	"testing"
)

// The colour engine forces TrueColor on Claude Code's non-tty pipe, degrades on CCPOOL_COLOR, and
// still yields to NO_COLOR / TERM=dumb. Only the truecolor default is pinned by the golden
// conformance suite; this pins the degraded tiers and the gates the goldens don't exercise.
func TestPaletteColorMatrix(t *testing.T) {
	const (
		tealTrue = "\x1b[38;2;86;182;194m"
		teal256  = "\x1b[38;5;73m"
		teal16   = "\x1b[36m"
		yellow   = "\x1b[93m" // already 16-colour -> unchanged across tiers
		red      = "\x1b[91m"
		dim      = "\x1b[2m"
	)
	cases := []struct {
		name                  string
		env                   map[string]string
		bar, yellow, red, dim string
	}{
		{"default forces truecolor", nil, tealTrue, yellow, red, dim},
		{"CCPOOL_COLOR=truecolor", map[string]string{"CCPOOL_COLOR": "truecolor"}, tealTrue, yellow, red, dim},
		{"CCPOOL_COLOR=256 degrades the bar", map[string]string{"CCPOOL_COLOR": "256"}, teal256, yellow, red, dim},
		{"CCPOOL_COLOR=16 degrades the bar", map[string]string{"CCPOOL_COLOR": "16"}, teal16, yellow, red, dim},
		{"CCPOOL_COLOR=ascii -> no colour", map[string]string{"CCPOOL_COLOR": "ascii"}, "", "", "", ""},
		{"unknown CCPOOL_COLOR fails open to truecolor", map[string]string{"CCPOOL_COLOR": "rainbow"}, tealTrue, yellow, red, dim},
		{"NO_COLOR beats a forced profile", map[string]string{"CCPOOL_COLOR": "truecolor", "NO_COLOR": "1"}, "", "", "", ""},
		{"empty NO_COLOR does NOT disable", map[string]string{"NO_COLOR": ""}, tealTrue, yellow, red, dim},
		{"TERM=dumb -> no colour", map[string]string{"TERM": "dumb"}, "", "", "", ""},
		{"CCPOOL_BAR_COLOR raw override, verbatim", map[string]string{"CCPOOL_BAR_COLOR": "\x1b[35m"}, "\x1b[35m", yellow, red, dim},
		{"CCPOOL_BAR_COLOR suppressed when colour off", map[string]string{"CCPOOL_BAR_COLOR": "\x1b[35m", "NO_COLOR": "1"}, "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Reset ambient colour env so a case's absent keys can't leak in (t.Setenv only restores
			// keys it sets), then apply this case.
			for _, k := range []string{"CCPOOL_COLOR", "NO_COLOR", "TERM", "CCPOOL_BAR_COLOR"} {
				os.Unsetenv(k)
			}
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			p := loadPalette()
			for _, f := range []struct{ field, got, want string }{
				{"bar", p.bar, c.bar},
				{"yellow", p.yellow, c.yellow},
				{"red", p.red, c.red},
				{"dim", p.dim, c.dim},
			} {
				if f.got != f.want {
					t.Errorf("%s = %q, want %q", f.field, f.got, f.want)
				}
			}
		})
	}
}
