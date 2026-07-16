// Package install manages julius integration with Claude Code settings:
// registering hooks, verifying the installation, and doing both safely
// (atomic writes, backups, idempotence).
package install

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// HookCommand is the command Claude Code runs on PreToolUse events.
const HookCommand = "julius hook claude-pre"

// PostHookCommand is the command Claude Code runs on PostToolUse events.
const PostHookCommand = "julius hook claude-post"

// PostHookMatcher limits PostToolUse to tools julius knows how to handle.
// Read events are used only for session-level deduplication of repeated
// reads — fresh file content is never rewritten.
const PostHookMatcher = "Bash|Grep|Glob|Read"

// PostHookMatcherMCP additionally routes MCP tool results through the post
// hook (opt-in via `julius init --mcp`). Opt-in because compressing an
// unfamiliar server's output is riskier than the native tools' known
// shapes; the hook still refuses to touch errors and non-JSON payloads.
const PostHookMatcherMCP = PostHookMatcher + "|mcp__.*"

// PatchMode controls whether Init modifies settings without asking.
type PatchMode int

const (
	// PatchAsk prompts the user before writing (default).
	PatchAsk PatchMode = iota
	// PatchAuto writes without prompting (CI / scripted installs).
	PatchAuto
	// PatchSkip only prints manual instructions.
	PatchSkip
)

// SettingsPath returns the Claude Code settings file Init would modify.
func SettingsPath(global bool, cwd string) (string, error) {
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".claude", "settings.json"), nil
	}
	return filepath.Join(cwd, ".claude", "settings.json"), nil
}

// Installed reports whether BOTH julius hooks are registered in the given
// settings file with the current matcher. Partial or outdated installs
// (missing post hook, stale matcher) report false so Init upgrades them
// in place. The base matcher is a substring of the MCP variant, so an
// MCP-extended install passes this check too.
func Installed(path string) bool {
	return installedWith(path, PostHookMatcher)
}

func installedWith(path, matcher string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	s := string(data)
	return strings.Contains(s, HookCommand) &&
		strings.Contains(s, PostHookCommand) &&
		strings.Contains(s, matcher)
}

// Init registers the julius hooks in Claude Code settings. With mcp, the
// PostToolUse matcher also covers MCP tools; running with mcp on an
// existing base install upgrades the matcher in place, and a plain re-run
// never downgrades an MCP-extended one (substring check).
func Init(global bool, mode PatchMode, mcp bool, cwd string, stdin io.Reader, stdout io.Writer) error {
	path, err := SettingsPath(global, cwd)
	if err != nil {
		return err
	}
	matcher := PostHookMatcher
	if mcp {
		matcher = PostHookMatcherMCP
	}

	if installedWith(path, matcher) {
		fmt.Fprintf(stdout, "julius hook already installed in %s\n", path)
		return nil
	}

	if mode == PatchSkip {
		printManual(stdout, path, matcher)
		return nil
	}
	if mode == PatchAsk {
		if !isTerminal(os.Stdin) {
			printManual(stdout, path, matcher)
			fmt.Fprintln(stdout, "\n(non-interactive session: re-run with --auto-patch to apply)")
			return nil
		}
		fmt.Fprintf(stdout, "Register the julius hook in %s? [y/N] ", path)
		var answer string
		_, _ = fmt.Fscanln(stdin, &answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "aborted — nothing was changed")
			return nil
		}
	}

	if err := patchSettings(path, matcher); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "julius hook registered in %s\nRestart Claude Code (or start a new session) to activate it.\n", path)
	return nil
}

// patchSettings appends the julius hook to hooks.PreToolUse, preserving
// every other field in the file. Atomic write with a .bak backup.
func patchSettings(path, postMatcher string) error {
	settings := map[string]any{}
	original, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(original, &settings); err != nil {
			return fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	case os.IsNotExist(err):
		original = nil
	default:
		return err
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	ensureHook(hooks, "PreToolUse", "Bash", HookCommand)
	ensureHook(hooks, "PostToolUse", postMatcher, PostHookCommand)
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if original != nil {
		if err := os.WriteFile(path+".bak", original, 0o644); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ensureHook appends an event entry unless the command is already present,
// keeping repeated Init runs idempotent per hook. When the command exists
// under an outdated matcher, the matcher is upgraded in place.
func ensureHook(hooks map[string]any, event, matcher, command string) {
	entries, _ := hooks[event].([]any)
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		raw, err := json.Marshal(entry)
		if err != nil || !strings.Contains(string(raw), command) {
			continue
		}
		entry["matcher"] = matcher
		return
	}
	entries = append(entries, map[string]any{
		"matcher": matcher,
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	})
	hooks[event] = entries
}

func printManual(w io.Writer, path, postMatcher string) {
	fmt.Fprintf(w, `Add this to %s under "hooks":

  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [{ "type": "command", "command": "%s" }]
    }
  ],
  "PostToolUse": [
    {
      "matcher": "%s",
      "hooks": [{ "type": "command", "command": "%s" }]
    }
  ]
`, path, HookCommand, postMatcher, PostHookCommand)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
