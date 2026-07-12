// Package initcmd is the `ccpool init` command: it wires ccpool into Claude Code's settings.json
// with zero required config. Dry-run by default (prints the plan, writes nothing); --apply merges
// after a timestamped backup. The merge is idempotent, never-clobber, and symlink-aware: when
// settings.json is a symlink to a dotfiles source we follow it and rewrite the REAL target, leaving
// the link intact. A dangling symlink (target missing) ABORTS rather than being renamed over and
// converted to a regular file (the clobber a reviewer caught in the Ruby original). init is
// on-demand, so it fails LOUD (returns an error / non-zero) — the opposite of the fail-open hot path.
//
// The observable behaviour (stdout + the resulting settings.json bytes) is byte-identical to the
// Ruby Init module, which is the conformance oracle (docs/GO-MIGRATION.md).
package initcmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SeanLF/ccpool/internal/fmtx"
	"github.com/SeanLF/ccpool/internal/statusline"
)

// warnEvents are the Claude Code hook events ccpool wires its warn hook into, in the order the
// plan reports and appends them.
var warnEvents = []string{"UserPromptSubmit", "PostToolUse"}

// launcherOverride lets the conformance test pin the launcher path to the Ruby reference
// (<repo>/bin/ccpool) so the wired command strings match byte-for-byte. Empty in production, where
// launcher() defaults to the running binary — post-migration ccpool IS the launcher.
var launcherOverride string

// launcher is the ccpool executable wired into settings so hooks resolve regardless of PATH.
func launcher() string {
	if launcherOverride != "" {
		return launcherOverride
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "ccpool"
}

func statuslineCmd() string { return launcher() + " statusline" }
func warnCmd() string       { return launcher() + " warn" }

// settingsPath resolves CCPOOL_SETTINGS (or the ~/.claude default) the way Ruby File.expand_path
// does: expand a leading ~, make relative paths absolute, and Clean — but do NOT resolve symlinks
// (realTarget does that). Resolved locally rather than in internal/paths, which stays untouched.
func settingsPath() string {
	v, ok := os.LookupEnv("CCPOOL_SETTINGS")
	if !ok || v == "" {
		v = "~/.claude/settings.json"
	}
	return expandPath(v)
}

func expandPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		if wd, err := os.Getwd(); err == nil {
			p = filepath.Join(wd, p)
		}
	}
	return filepath.Clean(p)
}

// --- detection (idempotency) ---

var (
	warnBoundary       = regexp.MustCompile(`\bwarn\b`)
	statuslineBoundary = regexp.MustCompile(`\bstatusline\b`)
)

// ccpoolCmd reports whether cmd invokes ccpool with verb. Matches both `ruby /x/ccpool.rb warn`
// and `/x/bin/ccpool warn`, so a hand-wired setup counts as already-done.
func ccpoolCmd(cmd any, boundary *regexp.Regexp) bool {
	s, ok := cmd.(string)
	return ok && strings.Contains(s, "ccpool") && boundary.MatchString(s)
}

func warnWired(settings *omap, event string) bool {
	groups, ok := dig(settings, "hooks", event).([]any)
	if !ok {
		return false
	}
	for _, g := range groups {
		gm, ok := g.(*omap)
		if !ok {
			continue
		}
		hooks, ok := gm.get("hooks")
		if !ok {
			continue
		}
		arr, ok := hooks.([]any)
		if !ok {
			continue
		}
		for _, h := range arr {
			hm, ok := h.(*omap)
			if !ok {
				continue
			}
			if c, ok := hm.get("command"); ok && ccpoolCmd(c, warnBoundary) {
				return true
			}
		}
	}
	return false
}

// slState is the statusLine ownership tier.
type slState int

const (
	slAbsent  slState = iota // no statusLine at all
	slOurs                   // already a ccpool statusline
	slForeign                // some other command we must not clobber
)

func statuslineState(settings *omap) slState {
	slv, ok := settings.get("statusLine")
	if !ok {
		return slAbsent
	}
	sl, ok := slv.(*omap)
	if !ok {
		return slAbsent
	}
	cmd, ok := sl.get("command")
	if !ok {
		return slAbsent
	}
	if _, isStr := cmd.(string); !isStr {
		return slAbsent
	}
	if ccpoolCmd(cmd, statuslineBoundary) {
		return slOurs
	}
	return slForeign
}

// ccstatuslineHost reports whether the statusLine is a ccstatusline host ccpool can compose into
// (a custom-command widget) rather than replace.
func ccstatuslineHost(settings *omap) bool {
	cmd, _ := dig(settings, "statusLine", "command").(string)
	return strings.Contains(cmd, "ccstatusline")
}

// --- plan (pure: settings -> what would change) ---

type plan struct {
	statusline         slState
	statuslineExisting string // the foreign command, when statusline == slForeign
	composeHost        bool
	addStatusline      bool
	conflict           bool
	hooksPresent       map[string]bool // event -> already wired
	addHooks           []string        // missing events, in warnEvents order
}

func makePlan(settings *omap, replaceStatusline bool) plan {
	sl := statuslineState(settings)
	present := make(map[string]bool, len(warnEvents))
	var addHooks []string
	for _, e := range warnEvents {
		p := warnWired(settings, e)
		present[e] = p
		if !p {
			addHooks = append(addHooks, e)
		}
	}
	compose := sl == slForeign && ccstatuslineHost(settings)
	pl := plan{
		statusline:    sl,
		composeHost:   compose,
		addStatusline: sl == slAbsent || (sl == slForeign && replaceStatusline && !compose),
		conflict:      sl == slForeign && !replaceStatusline,
		hooksPresent:  present,
		addHooks:      addHooks,
	}
	if sl == slForeign {
		pl.statuslineExisting, _ = dig(settings, "statusLine", "command").(string)
	}
	return pl
}

func (pl plan) changes() bool { return pl.addStatusline || len(pl.addHooks) > 0 }

// applyPlan mutates settings per the plan: adds ccpool's statusLine and/or appends the warn hook to
// each missing event, preserving every other key and pre-existing hook (never-clobber).
func applyPlan(settings *omap, pl plan) {
	if pl.addStatusline {
		sl := &omap{}
		sl.set("type", "command")
		sl.set("command", statuslineCmd())
		sl.set("refreshInterval", json.Number("10"))
		settings.set("statusLine", sl)
	}
	if len(pl.addHooks) == 0 {
		return
	}
	hooks := settings.getOrCreateObject("hooks")
	for _, event := range pl.addHooks {
		group := &omap{}
		hook := &omap{}
		hook.set("type", "command")
		hook.set("command", warnCmd())
		group.set("hooks", []any{hook})

		arr, _ := hooks.get(event)
		list, _ := arr.([]any)
		hooks.set(event, append(list, group))
	}
}

// --- IO ---

// realTarget follows the symlink so we edit the real dotfiles target, not replace the link. A
// missing file is a fresh install (the literal path, created on write). Mirrors Ruby File.realpath.
func realTarget(settings string) string {
	if _, err := os.Stat(settings); err != nil {
		return settings
	}
	if resolved, err := filepath.EvalSymlinks(settings); err == nil {
		return resolved
	}
	return settings
}

// danglingSymlink is a symlink whose target doesn't currently exist. os.Stat follows the link and
// errors here, so without this check init would mistake it for a fresh install and rename over the
// link, destroying it (the clobber this command promises to avoid). os.Lstat sees the link itself.
func danglingSymlink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = os.Stat(path)
	return err != nil
}

// loadSettings returns (nil, false) for no file (fresh) and (nil, true) for a present-but-unreadable
// file (not a JSON object): refuse to touch it.
func loadSettings(path string) (settings *omap, unreadable bool) {
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, true // present but unreadable (EACCES / a directory) -> refuse to touch, don't
		// mistake it for a fresh install (Ruby fails loud here; only a JSON parse error is rescued).
	}
	v, err := decodeOrdered(b)
	if err != nil {
		return nil, true
	}
	o, ok := v.(*omap)
	if !ok {
		return nil, true
	}
	return o, false
}

// backupSettings copies the file aside before a write, never overwriting an existing backup.
// Returns the backup path, or "" when there was no prior file.
func backupSettings(path string, now int64) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", nil
	}
	bak := fmt.Sprintf("%s.bak.%d", path, now)
	if _, err := os.Stat(bak); err == nil {
		bak = fmt.Sprintf("%s.%d", bak, os.Getpid())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(bak, b, 0o644); err != nil {
		return "", err
	}
	return bak, nil
}

// writeSettings atomically writes (tmp in the same dir + rename) so a crash can't leave a half file.
// path is the resolved real target, so a symlink pointing at it is preserved.
func writeSettings(path string, settings *omap) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(prettyGenerate(settings)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func which(cmd string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		full := filepath.Join(dir, cmd)
		if fi, err := os.Stat(full); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// ccusageLine reports (never blocks) whether the ccusage launcher is on PATH — ccpool works without
// it, just with a blank $ readout.
func ccusageLine() string {
	spec, ok := os.LookupEnv("CCPOOL_CCUSAGE_CMD")
	if !ok {
		spec = "npx -y ccusage@20"
	}
	first := ""
	if fields := strings.Fields(spec); len(fields) > 0 {
		first = fields[0]
	}
	if which(first) {
		return "ccusage: `" + first + "` found -> the $ value self-calibrates from a few days of usage history."
	}
	return "ccusage: `" + first + "` NOT found -> ccpool still works, but the $ readout stays blank until it's installed."
}

// --- rendering ---

func mark(sym, label, val string, note string) string {
	s := "  " + sym + " " + ljust(label, 22) + " " + val
	if note != "" {
		s += "\n      " + note
	}
	return s
}

func ljust(s string, n int) string {
	if len(s) < n {
		return s + strings.Repeat(" ", n-len(s))
	}
	return s
}

func renderHeader(settings, target string) string {
	hdr := "ccpool init -- wiring plan for " + settings
	if target != settings {
		hdr += "\n  real target: " + target + " (via symlink)"
	}
	return hdr
}

func renderPlan(pl plan) string {
	var lines []string
	switch pl.statusline {
	case slAbsent:
		lines = append(lines, mark("+", "statusLine", statuslineCmd(), "captures rate_limits + renders the pool line"))
	case slOurs:
		lines = append(lines, mark("=", "statusLine", "already wired to ccpool", ""))
	case slForeign:
		switch {
		case pl.composeHost:
			lines = append(lines, mark("*", "statusLine", "keep ccstatusline -- compose, don't replace",
				"add ccpool as a ccstatusline widget (recipe below) -- it keeps your line + adds the pool gauge"))
		case pl.addStatusline:
			lines = append(lines, mark("~", "statusLine", "REPLACE with ccpool", "was: "+pl.statuslineExisting))
		default:
			lines = append(lines, mark("!", "statusLine", "left as-is (a non-ccpool command is set)",
				"ccpool must own the statusLine to capture rate_limits -- re-run with --replace-statusline to take it over"))
		}
	}
	for _, e := range warnEvents {
		if pl.hooksPresent[e] {
			lines = append(lines, mark("=", e+" hook", "ccpool warn already present", ""))
		} else {
			lines = append(lines, mark("+", e+" hook", warnCmd(), "warn the agent mid-turn on pace / 5h / context"))
		}
	}
	return strings.Join(lines, "\n")
}

// composeRecipe prints the one manual step for a ccstatusline host: add ccpool as a widget.
func composeRecipe() {
	fmt.Println()
	fmt.Println("ccstatusline detected -- COMPOSE, don't replace. To add ccpool's pool gauge to your line:")
	fmt.Println("  1. open ccstatusline's config  (e.g. `npx ccstatusline`)")
	fmt.Println("  2. add a 'Custom Command' widget with command:")
	fmt.Println("       " + statuslineCmd() + " --embed")
	fmt.Println("  ccstatusline forwards Claude's full payload, so ccpool renders its $-left + pace inside your line.")
}

// preview shows what the statusline looks like from the freshest snapshot (init's final reassurance).
func preview(now int64) {
	fmt.Println()
	fmt.Println("Statusline preview:")
	previewStatusline(now)
}

// previewStatusline renders the freshest per-session snapshot, mirroring CCPool.preview_statusline.
// Best-effort: on no/unreadable snapshot it notes so on stderr and prints nothing to stdout.
func previewStatusline(now int64) {
	data, ok := statusline.NewestSnapshot()
	if !ok {
		fmt.Fprintln(os.Stderr, "ccpool: no statusline snapshot yet. Wire `ccpool statusline` as your Claude Code statusLine first (see README), then it self-populates.")
		return
	}
	age := now - statusline.SnapshotCapturedAt(data)
	line := statusline.Render(data, now)
	fmt.Fprintf(os.Stderr, "[preview from a %s-old snapshot -- ctx/cache may be stale; live values come from Claude Code]\n", fmtx.Dur(age))
	if line != "" {
		fmt.Println(line)
	}
}

// --- orchestration ---

// Run is `ccpool init`. Dry-run diff by default; --apply writes after a backup. It fails LOUD:
// returns an error (non-zero exit for the caller) on a dangling symlink or an unparseable file,
// aborting rather than clobbering either.
func Run(args []string, now int64) error {
	apply := contains(args, "--apply")
	replaceSL := contains(args, "--replace-statusline")

	settings := settingsPath()

	if danglingSymlink(settings) {
		link, _ := os.Readlink(settings)
		fmt.Fprintf(os.Stderr, "ccpool init: %s is a symlink to %s, which doesn't exist.\n", settings, link)
		fmt.Fprintln(os.Stderr, "Create or stow that target first so init edits the real file (not the link), then re-run.")
		return fmt.Errorf("ccpool init: %s is a dangling symlink", settings)
	}

	target := realTarget(settings)
	existing, unreadable := loadSettings(target)
	if unreadable {
		fmt.Fprintf(os.Stderr, "ccpool init: %s exists but isn't a JSON object -- refusing to touch it.\n", settings)
		fmt.Fprintln(os.Stderr, "Fix or move it, then re-run. (ccpool never overwrites a settings file it can't parse.)")
		return fmt.Errorf("ccpool init: %s is not a JSON object", settings)
	}
	if existing == nil {
		existing = &omap{}
	}
	// Refuse a settings file whose `hooks` is shaped so we can't merge without destroying data (a
	// non-object hooks, or a warn-event value that isn't an array). Ruby crashes on these before any
	// write; we abort loud the same way rather than let getOrCreateObject/append silently clobber.
	if hooksUnmergeable(existing) {
		fmt.Fprintf(os.Stderr, "ccpool init: %s has a 'hooks' entry ccpool can't merge into -- refusing to touch it.\n", settings)
		fmt.Fprintln(os.Stderr, "Fix or move it, then re-run. (ccpool never clobbers hooks it can't understand.)")
		return fmt.Errorf("ccpool init: unmergeable hooks in %s", settings)
	}

	pl := makePlan(existing, replaceSL)
	fmt.Println(renderHeader(settings, target))
	fmt.Println(renderPlan(pl))
	fmt.Println()
	fmt.Println(ccusageLine())
	if pl.composeHost {
		composeRecipe()
	}

	if !pl.changes() {
		fmt.Println()
		switch {
		case pl.composeHost:
			fmt.Println("settings.json is already fine -- just add the widget above and ccpool renders in your line.")
		case pl.conflict:
			fmt.Println("Your warn hooks are wired, but the statusLine points at a non-ccpool command, so")
			fmt.Println("ccpool can't capture rate_limits. Re-run `ccpool init --apply --replace-statusline`")
			fmt.Println("to take it over (your current one is backed up first).")
		default:
			fmt.Println("Already set up -- nothing to change.")
		}
		preview(now)
		return nil
	}

	if !apply {
		fmt.Println()
		fmt.Println("This is a DRY RUN -- nothing was written.")
		fmt.Println("Run `ccpool init --apply` to apply it (a timestamped backup is taken first).")
		return nil
	}

	backup, err := backupSettings(target, now)
	if err != nil {
		return err
	}
	applyPlan(existing, pl)
	if err := writeSettings(target, existing); err != nil {
		return err
	}

	fmt.Println()
	if backup != "" {
		fmt.Println("You're set up. Backup: " + backup)
	} else {
		fmt.Println("You're set up. (no prior settings to back up)")
	}
	if pl.composeHost {
		fmt.Println("warn hooks wired. Add the ccstatusline widget above and ccpool renders inside your line.")
	} else {
		fmt.Println("ccpool is now wired -- open Claude Code and it starts capturing your pool usage.")
	}
	preview(now)
	return nil
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// --- ordered JSON (preserve key order so the written settings.json is byte-identical to Ruby) ---

// omap is an insertion-ordered JSON object. Go maps don't preserve key order and encoding/json sorts
// keys, but Ruby JSON.pretty_generate emits hash keys in insertion order; matching it byte-for-byte
// (existing keys first, ccpool's additions appended) requires carrying the order explicitly.
type omap struct {
	keys []string
	vals []any
}

func (o *omap) get(k string) (any, bool) {
	for i, kk := range o.keys {
		if kk == k {
			return o.vals[i], true
		}
	}
	return nil, false
}

// set replaces the value at an existing key (keeping its position, as Ruby hash assignment does) or
// appends a new key.
func (o *omap) set(k string, v any) {
	for i, kk := range o.keys {
		if kk == k {
			o.vals[i] = v
			return
		}
	}
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
}

// hooksUnmergeable reports whether settings["hooks"] is present but shaped so ccpool can't merge
// into it without destroying data: a non-object hooks, or a warn-event whose value isn't an array.
// (An absent or null hooks is fine — a fresh object is created safely.)
func hooksUnmergeable(settings *omap) bool {
	v, ok := settings.get("hooks")
	if !ok || v == nil {
		return false
	}
	hooks, ok := v.(*omap)
	if !ok {
		return true // hooks is an array/string/number
	}
	for _, event := range warnEvents {
		ev, ok := hooks.get(event)
		if !ok || ev == nil {
			continue
		}
		if _, ok := ev.([]any); !ok {
			return true // this event's value isn't an array -> can't append
		}
	}
	return false
}

func (o *omap) getOrCreateObject(k string) *omap {
	if v, ok := o.get(k); ok {
		if m, ok := v.(*omap); ok {
			return m
		}
	}
	m := &omap{}
	o.set(k, m)
	return m
}

func dig(o *omap, keys ...string) any {
	var cur any = o
	for _, k := range keys {
		m, ok := cur.(*omap)
		if !ok {
			return nil
		}
		v, ok := m.get(k)
		if !ok {
			return nil
		}
		cur = v
	}
	return cur
}

// decodeOrdered parses JSON into ordered values (*omap for objects, []any for arrays, json.Number
// for numbers). Rejects trailing content, matching Ruby JSON.parse strictness.
func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing content after JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); ok {
		switch d {
		case '{':
			return decodeObject(dec)
		case '[':
			return decodeArray(dec)
		default:
			return nil, fmt.Errorf("unexpected delimiter %q", d)
		}
	}
	return t, nil // json.Number, string, bool, or nil
}

func decodeObject(dec *json.Decoder) (*omap, error) {
	o := &omap{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, fmt.Errorf("object key is not a string")
		}
		val, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		o.keys = append(o.keys, key)
		o.vals = append(o.vals, val)
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, err
	}
	return o, nil
}

func decodeArray(dec *json.Decoder) ([]any, error) {
	arr := []any{}
	for dec.More() {
		v, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return nil, err
	}
	return arr, nil
}

// prettyGenerate serializes ordered values exactly like Ruby JSON.pretty_generate: 2-space indent,
// ": " / ",\n" separators, empty containers inline ({} / []), no trailing newline, no "/" or
// non-ASCII escaping.
func prettyGenerate(v any) string {
	var b strings.Builder
	writeValue(&b, v, 0)
	return b.String()
}

func writeValue(b *strings.Builder, v any, depth int) {
	switch x := v.(type) {
	case *omap:
		writeObject(b, x, depth)
	case []any:
		writeArray(b, x, depth)
	case json.Number:
		b.WriteString(x.String())
	case string:
		b.WriteString(encodeString(x))
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	default:
		b.WriteString(encodeString(fmt.Sprint(x)))
	}
}

func writeObject(b *strings.Builder, o *omap, depth int) {
	if len(o.keys) == 0 {
		b.WriteString("{}")
		return
	}
	b.WriteString("{\n")
	inner := strings.Repeat("  ", depth+1)
	for i, k := range o.keys {
		b.WriteString(inner)
		b.WriteString(encodeString(k))
		b.WriteString(": ")
		writeValue(b, o.vals[i], depth+1)
		if i < len(o.keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte('}')
}

func writeArray(b *strings.Builder, arr []any, depth int) {
	if len(arr) == 0 {
		b.WriteString("[]")
		return
	}
	b.WriteString("[\n")
	inner := strings.Repeat("  ", depth+1)
	for i, v := range arr {
		b.WriteString(inner)
		writeValue(b, v, depth+1)
		if i < len(arr)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteByte(']')
}

// encodeString quotes and escapes a string the way Ruby's JSON generator does: standard escapes,
// control chars as \uXXXX, but no HTML escaping and no "/" escaping. Go's encoder with HTML escaping
// off matches this for the ASCII paths settings.json holds.
func encodeString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}
