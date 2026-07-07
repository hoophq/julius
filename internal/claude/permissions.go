// Package claude reads the pieces of Claude Code configuration julius
// must respect: permission rules for Bash commands and settings file
// locations for hook installation.
//
// Correctness contract: a julius rewrite must NEVER weaken the user's
// permission posture. Deny beats Ask beats Allow; when in doubt, the
// answer is "let Claude Code prompt the user".
package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Verdict is the permission outcome for a command under the user's rules.
type Verdict int

const (
	// VerdictNone means no rule matched; Claude Code's default applies (ask).
	VerdictNone Verdict = iota
	// VerdictAllow means an allow rule matched.
	VerdictAllow
	// VerdictAsk means an ask rule matched; the user must be prompted.
	VerdictAsk
	// VerdictDeny means a deny rule matched; julius must not touch the call.
	VerdictDeny
)

// Rules holds the user's Bash(...) permission rules across settings files.
type Rules struct {
	Allow []string
	Ask   []string
	Deny  []string
}

type settingsFile struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Ask   []string `json:"ask"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

// SettingsPaths returns the Claude Code settings files that can carry
// permission rules, in read order (global then project).
func SettingsPaths(projectDir string) []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".claude", "settings.json"),
			filepath.Join(home, ".claude", "settings.local.json"),
		)
	}
	paths = append(paths,
		filepath.Join(projectDir, ".claude", "settings.json"),
		filepath.Join(projectDir, ".claude", "settings.local.json"),
	)
	return paths
}

// LoadRules merges Bash permission rules from every settings file.
// Unreadable or malformed files are skipped: missing rules can only make
// julius more conservative elsewhere, never less safe here, because the
// hook still defers to Claude Code's own enforcement.
func LoadRules(projectDir string) Rules {
	var r Rules
	for _, p := range SettingsPaths(projectDir) {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var sf settingsFile
		if err := json.Unmarshal(data, &sf); err != nil {
			continue
		}
		r.Allow = append(r.Allow, sf.Permissions.Allow...)
		r.Ask = append(r.Ask, sf.Permissions.Ask...)
		r.Deny = append(r.Deny, sf.Permissions.Deny...)
	}
	return r
}

// Evaluate returns the verdict for a single (unchained) command.
// Precedence: Deny > Ask > Allow > None.
func (r Rules) Evaluate(cmd string) Verdict {
	if matchAnyRule(r.Deny, cmd) {
		return VerdictDeny
	}
	if matchAnyRule(r.Ask, cmd) {
		return VerdictAsk
	}
	if matchAnyRule(r.Allow, cmd) {
		return VerdictAllow
	}
	return VerdictNone
}

// EvaluateChain returns the most restrictive verdict across all segments
// of a command chain. Allow only survives if every segment allows.
func (r Rules) EvaluateChain(segments []string) Verdict {
	verdict := VerdictNone
	allAllow := len(segments) > 0
	for _, seg := range segments {
		v := r.Evaluate(seg)
		if v > verdict {
			verdict = v
		}
		if v != VerdictAllow {
			allAllow = false
		}
	}
	if verdict == VerdictAllow && !allAllow {
		return VerdictNone
	}
	return verdict
}

// matchAnyRule matches a command against Bash(...) permission rules.
// Supported forms (matching Claude Code's documented semantics):
//
//	Bash              — every command
//	Bash(*)           — every command
//	Bash(x)           — exact match
//	Bash(x:*)         — prefix match (x plus anything after)
//	Bash(x *)         — glob-style prefix: "x " followed by anything
func matchAnyRule(rules []string, cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if !strings.HasPrefix(rule, "Bash") {
			continue
		}
		if rule == "Bash" {
			return true
		}
		if !strings.HasPrefix(rule, "Bash(") || !strings.HasSuffix(rule, ")") {
			continue
		}
		spec := rule[len("Bash(") : len(rule)-1]
		if matchSpec(spec, cmd) {
			return true
		}
	}
	return false
}

func matchSpec(spec, cmd string) bool {
	switch {
	case spec == "*" || spec == "":
		return true
	case strings.HasSuffix(spec, ":*"):
		prefix := strings.TrimSuffix(spec, ":*")
		return cmd == prefix || strings.HasPrefix(cmd, prefix)
	case strings.HasSuffix(spec, " *"):
		prefix := strings.TrimSuffix(spec, "*")
		return strings.HasPrefix(cmd, prefix) || cmd == strings.TrimSpace(prefix)
	default:
		return cmd == spec
	}
}
