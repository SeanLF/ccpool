package main

import "testing"

// wantsHelp must stop at "--" so a flag for a wrapped command (`ccpool run -- foo --help`) is the
// child's, not a request for run's help.
func TestWantsHelpStopsAtSeparator(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"--help"}, true},
		{[]string{"-h"}, true},
		{[]string{"--history"}, false}, // a real flag, not help
		{[]string{"--", "echo", "--help"}, false},
		{[]string{"--help", "--", "x"}, true},
		{nil, false},
	}
	for _, c := range cases {
		if got := wantsHelp(c.args); got != c.want {
			t.Errorf("wantsHelp(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// Every human-facing command has a --help entry (hooks + internal commands don't need one).
func TestCommandHelpCoversHumanCommands(t *testing.T) {
	for _, cmd := range []string{"status", "check", "statusline", "warn", "run", "review", "rhythm", "init", "config", "prune"} {
		if _, ok := commandHelp[cmd]; !ok {
			t.Errorf("no --help entry for %q", cmd)
		}
	}
}
