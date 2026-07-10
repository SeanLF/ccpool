// Command ccpool helps get the most out of a fixed Claude subscription pool.
//
// This is the Go port of the original Ruby implementation (see docs/GO-MIGRATION.md).
// Subcommands are added phase by phase; the on-disk contract in docs/GO-MIGRATION.md is
// the durable interop boundary with any Ruby still running during the transition.
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/SeanLF/ccpool/internal/statusline"
	"github.com/SeanLF/ccpool/internal/warn"
)

// Build metadata, injected at release time by GoReleaser via -ldflags -X.
// Defaults keep `go run`/`go install` builds honest ("dev") rather than blank.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run is the dispatch core, kept separate from main so it is testable and returns
// an exit code rather than calling os.Exit. On-demand commands fail LOUD (non-zero);
// the hot-path hooks (statusline, warn) will fail OPEN via recover when they land.
func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}

	switch args[0] {
	case "statusline":
		embed := hasFlag(args[1:], "--embed") || hasFlag(args[1:], "--compact")
		statusline.Command(time.Now().Unix(), embed)
		return 0
	case "__warm-calib": // internal: detached background $/1% warm-up (see statusline warm)
		statusline.WarmCalib(time.Now().Unix())
		return 0
	case "warn":
		warn.Hook(time.Now().Unix())
		return 0
	case "version", "--version", "-v":
		fmt.Printf("ccpool %s (%s, built %s)\n", version, commit, date)
		return 0
	case "help", "--help", "-h":
		usage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "ccpool: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return 2
	}
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func usage(w io.Writer) {
	fmt.Fprint(w, `ccpool - get the most out of a fixed Claude subscription pool

Usage: ccpool <command> [args]

Commands:
  version    Print version, commit, and build date
  help       Show this help

More commands (statusline, warn, status, check, run, review, rhythm, init,
prune) are being ported from the Ruby reference; see docs/GO-MIGRATION.md.
`)
}
