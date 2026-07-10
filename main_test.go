package main

import (
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain registers the ccpool binary as a testscript command so the .txtar scripts under
// testdata/script exercise the REAL CLI dispatch layer (arg parsing, exit codes, stdout-vs-stderr
// routing) end to end -- the seam the golden conformance suites skip by calling status.Status /
// status.Report directly. `ccpool <args>` in a script re-enters dispatch with those args.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"ccpool": func() { os.Exit(dispatch(os.Args[1:])) },
	})
}

// TestCLIScripts runs every testdata/script/*.txtar. Scripts point every CCPOOL_*/USAGE_* path into
// the sandbox $WORK dir, so they never read or write the real ~/.claude. They assert on now-robust
// behaviour (no-data readouts, the init dry-run plan, help/version/unknown-command) rather than the
// live budget numbers, which dispatch derives from the wall clock.
func TestCLIScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/script",
	})
}
