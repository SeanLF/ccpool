# VHS tape -> demo/init.gif. Regenerate: `make demo` (or `vhs demo/init.tape` from repo root).
# `ccpool init` is dry-run by default: it prints exactly what it would wire, and writes nothing.
Output demo/init.gif

Set Shell "bash"
Set FontSize 16
Set Width 1500
Set Height 420
Set Padding 24
Set Theme "Catppuccin Mocha"

Hide
Type "source demo/setup.sh && clear" Enter
Sleep 800ms
Show

Type "# one command wires ccpool into Claude Code -- dry-run by default:" Enter
Sleep 900ms
Type "ccpool init" Enter
Sleep 4500ms
