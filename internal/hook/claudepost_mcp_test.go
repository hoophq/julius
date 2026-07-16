package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/ledger"
)

// runPostAny is runPost for outputs whose updatedToolOutput is not an
// object — MCP responses can be a bare content-block array.
func runPostAny(t *testing.T, input string, rec Recorder) any {
	t.Helper()
	reg := filter.Load(t.TempDir())
	var out bytes.Buffer
	ProcessPostToolUse(strings.NewReader(input), &out, reg, rec)
	if out.Len() == 0 {
		return nil
	}
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			UpdatedToolOutput any    `json:"updatedToolOutput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid hook JSON: %v", err)
	}
	return parsed.HookSpecificOutput.UpdatedToolOutput
}

// linearListJSON imitates a Linear list_issues result: null-heavy items
// with long descriptions — the shape measured in real transcripts.
func linearListJSON(items int) string {
	var sb strings.Builder
	sb.WriteString(`{"issues":[`)
	for i := 0; i < items; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb,
			`{"id":"ATR-%d","title":"issue %d","description":%q,"archivedAt":null,"completedAt":null,"slaStartedAt":null,"slaBreachesAt":null,"url":"https://linear.app/hoophq/issue/ATR-%d"}`,
			i, i, strings.Repeat("long description text ", 40), i)
	}
	sb.WriteString(`],"hasNextPage":false}`)
	return sb.String()
}

func mcpEvent(toolName string, response any) string {
	b, _ := json.Marshal(map[string]any{
		"session_id": "mcp-test", "tool_name": toolName,
		"tool_input":    map[string]any{"team": "ATR"},
		"tool_response": response,
	})
	return string(b)
}

func TestPostMCPArrayShapeCompressed(t *testing.T) {
	var ev *ledger.HookEvent
	rec := func(e ledger.HookEvent) { ev = &e }
	payload := linearListJSON(30)
	resp := []any{map[string]any{"type": "text", "text": payload}}

	updated := runPostAny(t, mcpEvent("mcp__linear-server__list_issues", resp), rec)
	blocks, ok := updated.([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("updatedToolOutput is not a 1-block array: %T %v", updated, updated)
	}
	block := blocks[0].(map[string]any)
	text, _ := block["text"].(string)
	if block["type"] != "text" || text == "" {
		t.Fatalf("block shape lost: %v", block)
	}
	if len(text) >= len(payload) {
		t.Errorf("text not compressed: %d >= %d", len(text), len(payload))
	}
	if !strings.Contains(text, "[julius] compacted JSON") {
		t.Errorf("honest marker missing: %s", text[len(text)-200:])
	}
	if !strings.Contains(text, "null fields dropped") || !strings.Contains(text, "array items omitted") {
		t.Errorf("marker does not disclose removals: %s", text[len(text)-200:])
	}
	if ev == nil || ev.Kind != "post_compress" || ev.Tool != "mcp__linear-server__list_issues" {
		t.Errorf("savings not recorded correctly: %+v", ev)
	}
	if ev != nil && ev.TokensAfter >= ev.TokensBefore {
		t.Errorf("no measured savings: %+v", ev)
	}
}

func TestPostMCPObjectShapePreservesWrapper(t *testing.T) {
	resp := map[string]any{
		"content":   []any{map[string]any{"type": "text", "text": linearListJSON(25)}},
		"isError":   false,
		"extraMeta": "must-survive",
	}
	updated := runPost(t, mcpEvent("mcp__linear-server__list_issues", resp), nil)
	if updated == nil {
		t.Fatal("object-shape response not compressed")
	}
	if updated["extraMeta"] != "must-survive" {
		t.Errorf("wrapper fields lost: %v", updated)
	}
	blocks, _ := updated["content"].([]any)
	if len(blocks) != 1 {
		t.Fatalf("content blocks lost: %v", updated)
	}
	text, _ := blocks[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "[julius] compacted JSON") {
		t.Error("content block not compacted")
	}
}

func TestPostMCPErrorsUntouched(t *testing.T) {
	resp := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": linearListJSON(25)}},
		"isError": true,
	}
	if out := runPostAny(t, mcpEvent("mcp__linear-server__get_issue", resp), nil); out != nil {
		t.Errorf("error result was compressed: %v", out)
	}
}

func TestPostMCPSmallAndNonJSONUntouched(t *testing.T) {
	small := []any{map[string]any{"type": "text", "text": `{"ok":true}`}}
	if out := runPostAny(t, mcpEvent("mcp__linear-server__save_issue", small), nil); out != nil {
		t.Errorf("small result was rewritten: %v", out)
	}
	prose := []any{map[string]any{"type": "text", "text": strings.Repeat("plain prose result, definitely not json. ", 40)}}
	if out := runPostAny(t, mcpEvent("mcp__docs-server__search", prose), nil); out != nil {
		t.Errorf("non-JSON text was rewritten: %v", out)
	}
}

func TestPostMCPNonTextBlocksSurvive(t *testing.T) {
	resp := []any{
		map[string]any{"type": "image", "data": "iVBORw0KGgo="},
		map[string]any{"type": "text", "text": linearListJSON(25)},
	}
	updated := runPostAny(t, mcpEvent("mcp__linear-server__list_issues", resp), nil)
	blocks, ok := updated.([]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("block count changed: %v", updated)
	}
	img := blocks[0].(map[string]any)
	if img["type"] != "image" || img["data"] != "iVBORw0KGgo=" {
		t.Errorf("non-text block mangled: %v", img)
	}
}
