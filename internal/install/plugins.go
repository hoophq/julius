package install

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxSourceRead caps how much of any settings/plugin file the source scan
// reads. The scan is best-effort diagnostics; a pathological file must not
// stall doctor or init.
const maxSourceRead = 256 << 10 // 256KB

// hookSource identifies one place that registers a julius hook with Claude
// Code: either a settings file (label = path) or a plugin (label =
// "plugin name@marketplace", with the resolved install path attached).
type hookSource struct {
	label       string
	pluginName  string // "name@marketplace" when the source is a plugin
	installPath string // plugin install dir when the source is a plugin
	// commands holds the matched hook command strings parsed from a
	// settings file's hooks section, so doctor can tell byte-identical
	// duplicate registrations — which Claude Code runs only once — from
	// genuinely distinct ones. Empty for plugin sources and for settings
	// files whose hooks section could not be parsed.
	commands []string
}

func (s hookSource) isPlugin() bool { return s.pluginName != "" }

// settingsCandidates returns the Claude Code settings files consulted for
// julius hooks, in ascending precedence order — user, project, project-local
// — deduplicated by canonical path: when the scan runs from the home
// directory, <home>/.claude/settings.json and <cwd>/.claude/settings.json
// are the same file and must be read (and counted) once.
func settingsCandidates(home, cwd string) []string {
	candidates := []string{
		filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range candidates {
		key := canonicalPath(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// canonicalPath returns the identity key used to deduplicate settings
// paths: absolute and cleaned, with symlinks resolved when the path exists.
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// hookSourceDetails returns every source that registers the julius pre/post
// hooks with Claude Code: settings files and installed plugins, with plugin
// metadata retained so callers can point at the plugin's install directory.
// Best-effort — unreadable or malformed files are skipped, never fatal.
func hookSourceDetails(home, cwd string) (pre, post []hookSource) {
	// settingsBodies stays in ascending precedence order (user, project,
	// project-local); pluginDisabled depends on it.
	var settingsBodies [][]byte
	for _, path := range settingsCandidates(home, cwd) {
		data, err := readCapped(path)
		if err != nil {
			continue
		}
		settingsBodies = append(settingsBodies, data)
		s := string(data)
		// Substring match, consistent with installedWith: the settings
		// file invokes julius by its literal hook command.
		label := tildePath(home, path)
		preCmds, postCmds := settingsHookCommands(data)
		if strings.Contains(s, HookCommand) {
			pre = append(pre, hookSource{label: label, commands: preCmds})
		}
		if strings.Contains(s, PostHookCommand) {
			post = append(post, hookSource{label: label, commands: postCmds})
		}
	}

	pluginPre, pluginPost := pluginHookSources(home, settingsBodies)
	return append(pre, pluginPre...), append(post, pluginPost...)
}

// settingsHookCommands parses a settings file's hooks section and returns
// the command strings that invoke each julius hook. Parse failures yield
// nil — the substring detection above still counts the file, doctor just
// loses the ability to prove two registrations identical.
func settingsHookCommands(data []byte) (pre, post []string) {
	var s struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := json.Unmarshal(data, &s); err != nil || s.Hooks == nil {
		return nil, nil
	}
	for _, cmd := range hookCommands(s.Hooks, "PreToolUse") {
		if strings.Contains(cmd, HookCommand) {
			pre = append(pre, cmd)
		}
	}
	for _, cmd := range hookCommands(s.Hooks, "PostToolUse") {
		if strings.Contains(cmd, PostHookCommand) {
			post = append(post, cmd)
		}
	}
	return pre, post
}

// pluginHookSources scans installed Claude Code plugins for hooks that
// invoke julius. A plugin's hooks.json rarely names julius directly — the
// hoop plugin, for example, runs ${CLAUDE_PLUGIN_ROOT}/scripts/post-tool.sh
// which execs `"$JULIUS" hook claude-post` — so plugin-root-relative
// scripts are read and matched one level deep. Deeper indirection (a
// script sourcing another script that runs julius) is not followed; the
// scan trades completeness for never executing anything.
func pluginHookSources(home string, settingsBodies [][]byte) (pre, post []hookSource) {
	data, err := readCapped(filepath.Join(home, ".claude", "plugins", "installed_plugins.json"))
	if err != nil {
		return nil, nil
	}
	var installed struct {
		Plugins map[string][]struct {
			InstallPath string `json:"installPath"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &installed); err != nil {
		return nil, nil
	}

	// Deterministic order for stable doctor output.
	names := make([]string, 0, len(installed.Plugins))
	for name := range installed.Plugins {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if pluginDisabled(name, settingsBodies) {
			continue
		}
		var preHit, postHit bool
		var hitPath string
		for _, entry := range installed.Plugins[name] {
			if entry.InstallPath == "" {
				continue
			}
			hookedPre, hookedPost := pluginInvokesJulius(entry.InstallPath)
			if (hookedPre || hookedPost) && hitPath == "" {
				hitPath = entry.InstallPath
			}
			preHit = preHit || hookedPre
			postHit = postHit || hookedPost
		}
		src := hookSource{label: "plugin " + name, pluginName: name, installPath: hitPath}
		if preHit {
			pre = append(pre, src)
		}
		if postHit {
			post = append(post, src)
		}
	}
	return pre, post
}

// pluginDisabled reports whether settings disable the plugin via
// enabledPlugins, honoring Claude Code precedence: settingsBodies is
// ordered ascending (user, project, project-local) and the highest-
// precedence file that mentions the plugin decides — a project-local
// `true` overrides a user-level `false` and vice versa. Absent entries
// count as enabled — Claude Code runs installed plugins unless told
// otherwise.
func pluginDisabled(name string, settingsBodies [][]byte) bool {
	for i := len(settingsBodies) - 1; i >= 0; i-- {
		var s struct {
			EnabledPlugins map[string]any `json:"enabledPlugins"`
		}
		if err := json.Unmarshal(settingsBodies[i], &s); err != nil {
			continue
		}
		if v, ok := s.EnabledPlugins[name]; ok {
			return v == false
		}
	}
	return false
}

// pluginInvokesJulius reads <installPath>/hooks/hooks.json and reports
// whether the plugin's PreToolUse/PostToolUse hooks invoke julius.
func pluginInvokesJulius(installPath string) (pre, post bool) {
	data, err := readCapped(filepath.Join(installPath, "hooks", "hooks.json"))
	if err != nil {
		return false, false
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, false
	}
	events := raw
	if nested, ok := raw["hooks"].(map[string]any); ok {
		events = nested
	}
	for _, cmd := range hookCommands(events, "PreToolUse") {
		if commandInvokesJulius(cmd, installPath, HookCommand, "hook claude-pre") {
			pre = true
			break
		}
	}
	for _, cmd := range hookCommands(events, "PostToolUse") {
		if commandInvokesJulius(cmd, installPath, PostHookCommand, "hook claude-post") {
			post = true
			break
		}
	}
	return pre, post
}

// hookCommands extracts the command strings registered under one event in
// a hooks.json events map.
func hookCommands(events map[string]any, event string) []string {
	var commands []string
	entries, _ := events[event].([]any)
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		hooks, _ := entry["hooks"].([]any)
		for _, h := range hooks {
			hook, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, ok := hook["command"].(string); ok {
				commands = append(commands, cmd)
			}
		}
	}
	return commands
}

// commandInvokesJulius reports whether one plugin hook command runs the
// julius hook for a specific event. A command containing the literal hook
// invocation (directNeedle, e.g. "julius hook claude-pre") is a direct
// hit — a bare "julius" mention is not enough, the event must match.
// Otherwise every token is scanned for a ${CLAUDE_PLUGIN_ROOT} script
// reference: leading VAR=value assignments are skipped and surrounding
// quotes stripped, so `bash ${CLAUDE_PLUGIN_ROOT}/x.sh`,
// `"${CLAUDE_PLUGIN_ROOT}/x.sh"`, and `FOO=1 ${CLAUDE_PLUGIN_ROOT}/x.sh`
// all resolve. Referenced scripts are read one level deep and matched
// against scriptNeedle ("hook claude-pre"/"hook claude-post" — plugin
// scripts usually reach the binary through a variable like "$JULIUS").
// Known false negatives: the ${CLAUDE_PLUGIN_ROOT:-default} default form
// is not expanded, and a script that merely sources another
// julius-running script is missed.
func commandInvokesJulius(command, installPath, directNeedle, scriptNeedle string) bool {
	if strings.Contains(command, directNeedle) {
		return true
	}
	tokens := strings.Fields(command)
	for len(tokens) > 0 && isEnvAssignment(tokens[0]) {
		tokens = tokens[1:]
	}
	for _, tok := range tokens {
		tok = strings.Trim(tok, `"'`)
		if !strings.Contains(tok, "${CLAUDE_PLUGIN_ROOT}") {
			continue
		}
		script, ok := resolvePluginScript(tok, installPath)
		if !ok {
			continue
		}
		data, err := readCapped(script)
		if err == nil && strings.Contains(string(data), scriptNeedle) {
			return true
		}
	}
	return false
}

// isEnvAssignment reports whether a token is a VAR=value environment
// assignment prefix, as in `FOO=1 cmd`.
func isEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range tok[:eq] {
		switch {
		case r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// resolvePluginScript expands ${CLAUDE_PLUGIN_ROOT} in a token and
// verifies the result stays inside the plugin's install tree. Containment
// is checked lexically first (refusing ../ escapes), then re-verified
// after symlink resolution when EvalSymlinks succeeds — a symlinked
// script cannot point the scan outside the tree; on EvalSymlinks failure
// the lexical check stands. Only regular files are eligible for reading:
// the scan must never open devices, FIFOs, or directories.
func resolvePluginScript(token, installPath string) (string, bool) {
	script := filepath.Clean(strings.ReplaceAll(token, "${CLAUDE_PLUGIN_ROOT}", installPath))
	root := filepath.Clean(installPath)
	if !containedIn(script, root) {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(script); err == nil {
		resolvedRoot := root
		if rr, err := filepath.EvalSymlinks(root); err == nil {
			resolvedRoot = rr
		}
		if !containedIn(resolved, resolvedRoot) {
			return "", false
		}
		script = resolved
	}
	info, err := os.Stat(script)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return script, true
}

// containedIn reports whether path sits at or under root, lexically.
func containedIn(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// readCapped reads at most maxSourceRead bytes of a file.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxSourceRead))
}

// tildePath abbreviates a path under home to ~/... for display. The label
// uses forward slashes on every platform so doctor output (and its tests)
// read identically across OSes.
func tildePath(home, path string) string {
	if home != "" && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + filepath.ToSlash(path[len(home):])
	}
	return path
}
