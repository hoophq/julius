package install

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hermeticEnv points every environment seam the install package (and its
// dependencies) honors — HOME for settings/plugin discovery, the raw-output
// stash, the savings ledger, the session cache — at throwaway locations, so
// tests never read or write the developer's real julius state.
func hermeticEnv(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads USERPROFILE on Windows
	t.Setenv("JULIUS_RAW_DIR", filepath.Join(home, "julius-raw"))
	t.Setenv("JULIUS_LEDGER", filepath.Join(home, "julius-ledger.db"))
	t.Setenv("JULIUS_SESSION_DIR", filepath.Join(home, "julius-session"))
	return home
}

// writeTestFile writes content, creating parent directories.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeJuliusSettingsFile registers both julius hooks at path the way a
// real `julius init` would.
func writeJuliusSettingsFile(t *testing.T, path string) {
	t.Helper()
	writeTestFile(t, path, fmt.Sprintf(`{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "%s" }] }
    ],
    "PostToolUse": [
      { "matcher": "%s", "hooks": [{ "type": "command", "command": "%s" }] }
    ]
  }
}`, HookCommand, PostHookMatcher, PostHookCommand))
}

// writeJuliusSettings registers both julius hooks in dir/.claude/settings.json.
func writeJuliusSettings(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, ".claude", "settings.json")
	writeJuliusSettingsFile(t, path)
	return path
}

// writePluginFixture installs a fake plugin under a temp HOME, mirroring the
// hoop plugin's layout: hooks.json never names julius — its commands run
// ${CLAUDE_PLUGIN_ROOT} scripts that exec the julius hooks.
func writePluginFixture(t *testing.T, home, name string) string {
	t.Helper()
	installPath := filepath.Join(home, ".claude", "plugins", "cache", "fixture", name)
	writeTestFile(t, filepath.Join(installPath, "hooks", "hooks.json"), `{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Bash", "hooks": [{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/scripts/pre-tool.sh" }] }
    ],
    "PostToolUse": [
      { "matcher": "Bash|Grep|Glob|Read", "hooks": [{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/scripts/post-tool.sh" }] }
    ]
  }
}`)
	writeTestFile(t, filepath.Join(installPath, "scripts", "pre-tool.sh"),
		"#!/bin/sh\nexec \"$J\" hook claude-pre\n")
	writeTestFile(t, filepath.Join(installPath, "scripts", "post-tool.sh"),
		"#!/bin/sh\nexec \"$J\" hook claude-post\n")
	writeInstalledPlugins(t, home, name, installPath)
	return installPath
}

// writeInstalledPlugins registers one plugin in installed_plugins.json.
func writeInstalledPlugins(t *testing.T, home, name, installPath string) {
	t.Helper()
	writeTestFile(t, filepath.Join(home, ".claude", "plugins", "installed_plugins.json"),
		fmt.Sprintf(`{"plugins": {%q: [{"installPath": %q}]}}`, name, installPath))
}

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q not found in %+v", name, checks)
	return Check{}
}

func hasCheck(checks []Check, name string) bool {
	for _, c := range checks {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestDoctorWarnsOnDuplicateHookSources(t *testing.T) {
	home := hermeticEnv(t)
	cwd := t.TempDir()
	writeJuliusSettings(t, home)
	writePluginFixture(t, home, "hoop@hooplabs")

	checks := Doctor(cwd)

	reg := findCheck(t, checks, "Claude Code hook registered")
	if !reg.OK || reg.Detail != "~/.claude/settings.json" {
		t.Errorf("registration check = %+v, want PASS at ~/.claude/settings.json", reg)
	}

	src := findCheck(t, checks, "julius hook sources")
	if !src.OK || !src.Warn {
		t.Fatalf("sources check = %+v, want OK warn", src)
	}
	want := "2 sources invoke julius hook claude-post: ~/.claude/settings.json + plugin hoop@hooplabs — julius dedups the double event, but you pay two hook round-trips; remove the julius entries from settings.json (the plugin already runs them)"
	if src.Detail != want {
		t.Errorf("sources detail:\n got %q\nwant %q", src.Detail, want)
	}
}

func TestDoctorPluginOnlyRegistrationPasses(t *testing.T) {
	home := hermeticEnv(t)
	cwd := t.TempDir()
	installPath := writePluginFixture(t, home, "hoop@hooplabs")

	checks := Doctor(cwd)

	reg := findCheck(t, checks, "Claude Code hook registered")
	if !reg.OK {
		t.Errorf("plugin-only install must pass registration, got %+v", reg)
	}
	want := fmt.Sprintf("via plugin hoop@hooplabs (%s)", installPath)
	if reg.Detail != want {
		t.Errorf("registration detail = %q, want %q", reg.Detail, want)
	}

	src := findCheck(t, checks, "julius hook sources")
	if src.Warn {
		t.Errorf("single plugin source must not warn: %+v", src)
	}
	if src.Detail != "1 source per event (plugin hoop@hooplabs)" {
		t.Errorf("sources detail = %q", src.Detail)
	}
}

func TestDoctorSettingsOnlyNoWarn(t *testing.T) {
	home := hermeticEnv(t)
	cwd := t.TempDir()
	writeJuliusSettings(t, home)

	checks := Doctor(cwd)

	src := findCheck(t, checks, "julius hook sources")
	if src.Warn {
		t.Errorf("single settings source must not warn: %+v", src)
	}
	if src.Detail != "1 source per event (~/.claude/settings.json)" {
		t.Errorf("sources detail = %q", src.Detail)
	}
}

func TestDoctorFromHomeDirNoDuplicateWarn(t *testing.T) {
	home := hermeticEnv(t)
	writeJuliusSettings(t, home)

	// cwd == home: <home>/.claude/settings.json and <cwd>/.claude/settings.json
	// are the same file and must be scanned (and counted) exactly once.
	checks := Doctor(home)

	reg := findCheck(t, checks, "Claude Code hook registered")
	if !reg.OK || reg.Detail != "~/.claude/settings.json" {
		t.Errorf("registration check = %+v, want PASS at ~/.claude/settings.json", reg)
	}
	src := findCheck(t, checks, "julius hook sources")
	if src.Warn {
		t.Errorf("doctor run from the home directory double-counted settings.json: %+v", src)
	}
	if src.Detail != "1 source per event (~/.claude/settings.json)" {
		t.Errorf("sources detail = %q", src.Detail)
	}
}

func TestDoctorLocalSettingsOnlyConsistent(t *testing.T) {
	hermeticEnv(t)
	cwd := t.TempDir()
	localPath := filepath.Join(cwd, ".claude", "settings.local.json")
	writeJuliusSettingsFile(t, localPath)

	checks := Doctor(cwd)

	// Registration and sources must agree on the same file set: hooks that
	// live only in settings.local.json are a healthy single-source install,
	// not a contradiction (FAIL registration + one detected source).
	reg := findCheck(t, checks, "Claude Code hook registered")
	if !reg.OK || reg.Detail != localPath {
		t.Errorf("registration check = %+v, want PASS at %s", reg, localPath)
	}
	src := findCheck(t, checks, "julius hook sources")
	if src.Warn {
		t.Errorf("single local-settings source must not warn: %+v", src)
	}
	if want := fmt.Sprintf("1 source per event (%s)", localPath); src.Detail != want {
		t.Errorf("sources detail = %q, want %q", src.Detail, want)
	}
}

func TestDoctorSettingsOnlyIdenticalDuplicateIsHarmless(t *testing.T) {
	home := hermeticEnv(t)
	cwd := t.TempDir()
	writeJuliusSettings(t, home)
	cwdPath := writeJuliusSettings(t, cwd)

	checks := Doctor(cwd)

	src := findCheck(t, checks, "julius hook sources")
	if !src.Warn {
		t.Fatalf("duplicate settings registrations must warn: %+v", src)
	}
	// Claude Code dedups identical command strings across settings files,
	// so this duplicate costs nothing — the round-trip claim would be false.
	if strings.Contains(src.Detail, "round-trips") {
		t.Errorf("identical settings-only duplicate must not claim extra round-trips: %q", src.Detail)
	}
	for _, needle := range []string{"harmless but redundant", "~/.claude/settings.json", cwdPath} {
		if !strings.Contains(src.Detail, needle) {
			t.Errorf("sources detail missing %q: %q", needle, src.Detail)
		}
	}
}

func TestDoctorWarnAdviceIsPerEvent(t *testing.T) {
	home := hermeticEnv(t)
	cwd := t.TempDir()
	writeJuliusSettings(t, home)

	// Plugin covering ONLY the post hook: claude-pre has a single source
	// and must not be mentioned, and the "remove settings entries" advice
	// may only target the event the plugin also covers.
	installPath := filepath.Join(home, ".claude", "plugins", "cache", "fixture", "postonly@mkt")
	writeTestFile(t, filepath.Join(installPath, "hooks", "hooks.json"), `{
  "hooks": {
    "PostToolUse": [
      { "hooks": [{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/scripts/post-tool.sh" }] }
    ]
  }
}`)
	writeTestFile(t, filepath.Join(installPath, "scripts", "post-tool.sh"),
		"#!/bin/sh\nexec \"$J\" hook claude-post\n")
	writeInstalledPlugins(t, home, "postonly@mkt", installPath)

	checks := Doctor(cwd)

	src := findCheck(t, checks, "julius hook sources")
	if !src.Warn {
		t.Fatalf("settings+plugin post duplicate must warn: %+v", src)
	}
	if strings.Contains(src.Detail, "claude-pre") {
		t.Errorf("claude-pre is not duplicated and must not appear in the warning: %q", src.Detail)
	}
	if !strings.Contains(src.Detail, "claude-post") || !strings.Contains(src.Detail, "plugin postonly@mkt") {
		t.Errorf("warning must name the duplicated post event and the plugin: %q", src.Detail)
	}
}

func TestDoctorIgnoresDisabledPlugin(t *testing.T) {
	home := hermeticEnv(t)
	cwd := t.TempDir()
	writePluginFixture(t, home, "hoop@hooplabs")
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"enabledPlugins": {"hoop@hooplabs": false}}`)

	checks := Doctor(cwd)

	reg := findCheck(t, checks, "Claude Code hook registered")
	if reg.OK {
		t.Errorf("disabled plugin must not count as a registration: %+v", reg)
	}
	if hasCheck(checks, "julius hook sources") {
		t.Error("no active sources — the sources check must be omitted")
	}
}

func TestPluginDisabledPrecedence(t *testing.T) {
	home := hermeticEnv(t)
	cwd := t.TempDir()
	writePluginFixture(t, home, "hoop@hooplabs")

	// User settings disable, project-local re-enables: the local file has
	// the highest precedence and wins.
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"enabledPlugins": {"hoop@hooplabs": false}}`)
	writeTestFile(t, filepath.Join(cwd, ".claude", "settings.local.json"),
		`{"enabledPlugins": {"hoop@hooplabs": true}}`)
	pre, post := hookSourceDetails(home, cwd)
	if len(pre) != 1 || len(post) != 1 {
		t.Errorf("locally re-enabled plugin must count: pre=%v post=%v", pre, post)
	}

	// The reverse: user enables, project-local disables — local still wins.
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"enabledPlugins": {"hoop@hooplabs": true}}`)
	writeTestFile(t, filepath.Join(cwd, ".claude", "settings.local.json"),
		`{"enabledPlugins": {"hoop@hooplabs": false}}`)
	pre, post = hookSourceDetails(home, cwd)
	if len(pre) != 0 || len(post) != 0 {
		t.Errorf("locally disabled plugin must not count: pre=%v post=%v", pre, post)
	}

	// Middle layer: project settings.json disables over a user-level enable.
	if err := os.Remove(filepath.Join(cwd, ".claude", "settings.local.json")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(cwd, ".claude", "settings.json"),
		`{"enabledPlugins": {"hoop@hooplabs": false}}`)
	pre, post = hookSourceDetails(home, cwd)
	if len(pre) != 0 || len(post) != 0 {
		t.Errorf("project-disabled plugin must not count: pre=%v post=%v", pre, post)
	}
}

func TestPluginDirectHitRequiresEventNeedle(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	installPath := filepath.Join(home, ".claude", "plugins", "cache", "fixture", "bare@mkt")
	// Bare "julius" mentions (wrong subcommand, wrong event) must not count;
	// only the literal event-specific hook invocation is a direct hit.
	writeTestFile(t, filepath.Join(installPath, "hooks", "hooks.json"), `{
  "hooks": {
    "PreToolUse": [
      { "hooks": [{ "type": "command", "command": "julius doctor" }] },
      { "hooks": [{ "type": "command", "command": "julius hook claude-post" }] }
    ],
    "PostToolUse": [
      { "hooks": [{ "type": "command", "command": "julius hook claude-post --quiet" }] }
    ]
  }
}`)
	writeInstalledPlugins(t, home, "bare@mkt", installPath)

	pre, post := hookSourceDetails(home, cwd)
	if len(pre) != 0 {
		t.Errorf("bare/wrong-event julius mentions counted as pre sources: %v", pre)
	}
	if len(post) != 1 {
		t.Errorf("direct post invocation not detected: %v", post)
	}
}

func TestPluginInterpreterAndEnvPrefixedScripts(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	installPath := filepath.Join(home, ".claude", "plugins", "cache", "fixture", "wrapped@mkt")
	// The script token is not first: interpreter prefix + quotes for pre,
	// VAR=value env prefix for post. All must resolve.
	writeTestFile(t, filepath.Join(installPath, "hooks", "hooks.json"), `{
  "hooks": {
    "PreToolUse": [
      { "hooks": [{ "type": "command", "command": "bash \"${CLAUDE_PLUGIN_ROOT}/scripts/pre-tool.sh\"" }] }
    ],
    "PostToolUse": [
      { "hooks": [{ "type": "command", "command": "JULIUS_QUIET=1 ${CLAUDE_PLUGIN_ROOT}/scripts/post-tool.sh" }] }
    ]
  }
}`)
	writeTestFile(t, filepath.Join(installPath, "scripts", "pre-tool.sh"),
		"#!/bin/sh\nexec \"$J\" hook claude-pre\n")
	writeTestFile(t, filepath.Join(installPath, "scripts", "post-tool.sh"),
		"#!/bin/sh\nexec \"$J\" hook claude-post\n")
	writeInstalledPlugins(t, home, "wrapped@mkt", installPath)

	pre, post := hookSourceDetails(home, cwd)
	if len(pre) != 1 {
		t.Errorf("interpreter-prefixed quoted script not detected as pre source: %v", pre)
	}
	if len(post) != 1 {
		t.Errorf("env-prefixed script not detected as post source: %v", post)
	}
}

func TestHookSourcesPathTraversalGuard(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	installPath := filepath.Join(home, ".claude", "plugins", "cache", "fixture", "evil@mkt")
	writeTestFile(t, filepath.Join(installPath, "hooks", "hooks.json"), `{
  "hooks": {
    "PostToolUse": [
      { "hooks": [{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/../outside.sh" }] }
    ]
  }
}`)
	// The referenced script escapes the plugin's install tree; it must
	// never be read, even though it would match.
	writeTestFile(t, filepath.Join(installPath, "..", "outside.sh"),
		"#!/bin/sh\nexec julius hook claude-post\n")
	writeInstalledPlugins(t, home, "evil@mkt", installPath)

	pre, post := hookSourceDetails(home, cwd)
	if len(pre) != 0 || len(post) != 0 {
		t.Errorf("traversal script counted as a source: pre=%v post=%v", pre, post)
	}
}

func TestPluginSymlinkEscapeGuard(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	installPath := filepath.Join(home, ".claude", "plugins", "cache", "fixture", "sneaky@mkt")
	writeTestFile(t, filepath.Join(installPath, "hooks", "hooks.json"), `{
  "hooks": {
    "PostToolUse": [
      { "hooks": [{ "type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/scripts/link.sh" }] }
    ]
  }
}`)
	// A lexically in-tree path that symlinks out of the install tree must
	// be refused after resolution.
	outside := filepath.Join(home, "outside.sh")
	writeTestFile(t, outside, "#!/bin/sh\nexec julius hook claude-post\n")
	if err := os.MkdirAll(filepath.Join(installPath, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(installPath, "scripts", "link.sh")); err != nil {
		t.Skipf("cannot create symlinks on this platform: %v", err)
	}
	writeInstalledPlugins(t, home, "sneaky@mkt", installPath)

	pre, post := hookSourceDetails(home, cwd)
	if len(pre) != 0 || len(post) != 0 {
		t.Errorf("symlink-escaping script counted as a source: pre=%v post=%v", pre, post)
	}
}

func TestRenderWarnDoesNotFailOverall(t *testing.T) {
	var buf bytes.Buffer
	checks := []Check{
		{Name: "some check", OK: true, Detail: "fine"},
		{Name: "julius hook sources", OK: true, Warn: true, Detail: "2 sources"},
	}
	if ok := Render(checks, &buf); !ok {
		t.Error("warn-only checks must not fail the overall result")
	}
	out := buf.String()
	if !strings.Contains(out, "WARN") {
		t.Errorf("output missing WARN mark:\n%s", out)
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("warn rendered as FAIL:\n%s", out)
	}
}
