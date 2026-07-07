// Package hook implements the Claude Code hook protocol.
//
// Non-blocking guarantee: hook processing must never break the agent.
// Every path — malformed input, unknown tools, internal errors — produces
// either valid hook JSON or no output at all, and the caller always exits 0.
package hook

import (
	"encoding/json"
	"io"

	"github.com/hoophq/julius/internal/claude"
	"github.com/hoophq/julius/internal/router"
)

type preToolUseInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	CWD           string `json:"cwd"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

type hookSpecificOutput struct {
	HookEventName            string          `json:"hookEventName"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
}

type preToolUseOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

// ProcessPreToolUse reads a PreToolUse event from r and, when the Bash
// command has julius equivalents, writes the rewrite decision to w.
// Writing nothing means "no opinion" and Claude Code proceeds unchanged.
//
// Permission mapping (Deny > Ask > Allow, evaluated on the ORIGINAL
// command so rewrites can't weaken the user's rules):
//
//	deny  → no output; Claude Code's own deny rule fires
//	ask   → updatedInput only; the user still gets prompted
//	allow → updatedInput + permissionDecision allow
//	none  → updatedInput only (Claude Code default applies)
func ProcessPreToolUse(r io.Reader, w io.Writer, routable router.Matcher) {
	var in preToolUseInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return
	}
	if in.ToolName != "Bash" || in.ToolInput.Command == "" {
		return
	}

	routed, changed := router.Route(in.ToolInput.Command, routable)
	if !changed {
		return
	}

	rules := claude.LoadRules(in.CWD)
	var segments []string
	for _, p := range router.SplitChain(in.ToolInput.Command) {
		if p.Text != "" {
			segments = append(segments, p.Text)
		}
	}

	out := preToolUseOutput{hookSpecificOutput{
		HookEventName: "PreToolUse",
	}}

	switch rules.EvaluateChain(segments) {
	case claude.VerdictDeny:
		// Hands off: let Claude Code's native deny handle the original.
		return
	case claude.VerdictAllow:
		out.HookSpecificOutput.PermissionDecision = "allow"
		out.HookSpecificOutput.PermissionDecisionReason = "julius rewrite (allowed by your permission rules)"
	}

	updated, err := json.Marshal(map[string]string{"command": routed})
	if err != nil {
		return
	}
	out.HookSpecificOutput.UpdatedInput = updated

	enc := json.NewEncoder(w)
	_ = enc.Encode(out)
}
