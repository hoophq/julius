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

// Installed reports whether the julius hook is registered in the given
// settings file.
func Installed(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), HookCommand)
}

// Init registers the julius PreToolUse hook in Claude Code settings.
func Init(global bool, mode PatchMode, cwd string, stdin io.Reader, stdout io.Writer) error {
	path, err := SettingsPath(global, cwd)
	if err != nil {
		return err
	}

	if Installed(path) {
		fmt.Fprintf(stdout, "julius hook already installed in %s\n", path)
		return nil
	}

	if mode == PatchSkip {
		printManual(stdout, path)
		return nil
	}
	if mode == PatchAsk {
		if !isTerminal(os.Stdin) {
			printManual(stdout, path)
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

	if err := patchSettings(path); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "julius hook registered in %s\nRestart Claude Code (or start a new session) to activate it.\n", path)
	return nil
}

// patchSettings appends the julius hook to hooks.PreToolUse, preserving
// every other field in the file. Atomic write with a .bak backup.
func patchSettings(path string) error {
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
	pre, _ := hooks["PreToolUse"].([]any)
	pre = append(pre, map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{"type": "command", "command": HookCommand},
		},
	})
	hooks["PreToolUse"] = pre
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

func printManual(w io.Writer, path string) {
	fmt.Fprintf(w, `Add this to %s under "hooks":

  "PreToolUse": [
    {
      "matcher": "Bash",
      "hooks": [{ "type": "command", "command": "%s" }]
    }
  ]
`, path, HookCommand)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
