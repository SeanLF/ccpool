# VHS tape -> demo/overview.gif. Regenerate: `make demo` (or `vhs demo/overview.tape` from repo root).
# Renders against staged fixtures (demo/setup.sh) so the numbers are deterministic, never your real pool.
Output demo/overview.gif

Set Shell "bash"
Set FontSize 16
Set Width 1500
Set Height 300
Set Padding 24
Set Theme "Catppuccin Mocha"

Hide
Type "source demo/setup.sh && clear" Enter
Sleep 800ms
Show

Type "# the statusline ccpool renders inside Claude Code:" Enter
Sleep 900ms
Type "cat demo/.data/payload.json | ccpool statusline; echo" Enter
Sleep 2500ms

Type "# the full readout -- $ of pool left, pace, burn, working-hours runway:" Enter
Sleep 900ms
Type "ccpool status" Enter
Sleep 4000ms
