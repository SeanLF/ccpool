# VHS tape -> demo/check.png (a static screenshot of the `ccpool check` verdict). Regenerate: `make demo`.
# Renders against the staged fixtures (demo/setup.sh) so the numbers are deterministic.
Output demo/_check.gif

Set Shell "bash"
Set FontSize 16
Set Width 1500
Set Height 460
Set Padding 24
Set Theme "Catppuccin Mocha"

Hide
Type "source demo/setup.sh && clear" Enter
Sleep 800ms
Show

Type "# time + budget + a keep-going/stop verdict, for long or autonomous loops:" Enter
Sleep 700ms
Type "ccpool check" Enter
Sleep 3500ms
Screenshot demo/check.png
