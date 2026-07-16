package filter

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCompactJSONNonJSONPassthrough(t *testing.T) {
	for _, raw := range []string{"plain prose, not json", `{"broken": `, `42 trailing garbage`, `"just a string"`, `12345`} {
		res := Finalize(raw, CompactJSON(raw))
		if res.Output != raw {
			t.Errorf("non-compactable input %q was modified: %q", raw, res.Output)
		}
	}
}

func TestCompactJSONDropsNullsAndTruncates(t *testing.T) {
	long := strings.Repeat("d", 600)
	raw := fmt.Sprintf(`{
  "id": "ATR-78",
  "title": "issue",
  "description": %q,
  "archivedAt": null,
  "completedAt": null,
  "slaBreachesAt": null
}`, long)
	res := Finalize(raw, CompactJSON(raw))
	if !res.Applied {
		t.Fatal("valid JSON object not compacted")
	}
	jsonPart, _, _ := strings.Cut(res.Output, "\n[julius]")
	if strings.Contains(jsonPart, "archivedAt") || strings.Contains(jsonPart, "null") {
		t.Errorf("null fields survived: %s", jsonPart)
	}
	if strings.Contains(res.Output, long) {
		t.Error("600-rune description not truncated")
	}
	if !strings.Contains(res.Output, "3 null fields dropped") || !strings.Contains(res.Output, "1 long strings truncated") {
		t.Errorf("marker missing or wrong: %s", res.Output)
	}
	if len(res.Output) >= len(raw) {
		t.Errorf("output not smaller: %d >= %d", len(res.Output), len(raw))
	}
}

func TestCompactJSONCapsArrays(t *testing.T) {
	items := make([]string, 30)
	for i := range items {
		items[i] = fmt.Sprintf(`{"id":"ATR-%d","title":"issue number %d with some padding text"}`, i, i)
	}
	raw := `{"issues":[` + strings.Join(items, ",") + `]}`
	res := Finalize(raw, CompactJSON(raw))
	if !res.Applied {
		t.Fatal("array payload not compacted")
	}
	if !strings.Contains(res.Output, "10 array items omitted") {
		t.Errorf("cap marker missing: %s", res.Output)
	}
	if strings.Contains(res.Output, `"ATR-25"`) {
		t.Error("items past the cap survived")
	}
	if !strings.Contains(res.Output, `"ATR-19"`) {
		t.Error("items within the cap were lost")
	}
}

func TestCompactJSONProtectedKeysSurvive(t *testing.T) {
	longURL := "https://example.com/" + strings.Repeat("p/", 300)
	longDesc := strings.Repeat("x", 600)
	raw := fmt.Sprintf(`{"url": %q, "gitBranchName": "short", "description": %q}`, longURL, longDesc)
	res := Finalize(raw, CompactJSON(raw))
	if !res.Applied {
		t.Fatal("not compacted")
	}
	if !strings.Contains(res.Output, longURL) {
		t.Error("protected url was truncated")
	}
	if strings.Contains(res.Output, longDesc) {
		t.Error("unprotected long string survived")
	}
}

func TestCompactJSONNumbersRoundTrip(t *testing.T) {
	raw := `{"runId": 29123153293, "ratio": 1.25, "big": 9007199254740993, "pad": "` + strings.Repeat("p", 600) + `"}`
	res := Finalize(raw, CompactJSON(raw))
	if !res.Applied {
		t.Fatal("not compacted")
	}
	for _, n := range []string{"29123153293", "1.25", "9007199254740993"} {
		if !strings.Contains(res.Output, n) {
			t.Errorf("number %s did not round-trip: %s", n, res.Output)
		}
	}
}

func TestCompactJSONArrayNullElementsKept(t *testing.T) {
	raw := `{"cells": [null, "a", null, "b"], "gone": null, "pad": "` + strings.Repeat("p", 600) + `"}`
	res := Finalize(raw, CompactJSON(raw))
	if !res.Applied {
		t.Fatal("not compacted")
	}
	var parsed struct {
		Cells []any `json:"cells"`
	}
	jsonPart, _, _ := strings.Cut(res.Output, "\n[julius]")
	if err := json.Unmarshal([]byte(jsonPart), &parsed); err != nil {
		t.Fatalf("compacted output is not valid JSON: %v\n%s", err, jsonPart)
	}
	if len(parsed.Cells) != 4 {
		t.Errorf("array null elements dropped: %v", parsed.Cells)
	}
}

func TestCompactJSONWhitespaceOnlyWin(t *testing.T) {
	// Pretty-printed but otherwise untouchable JSON still compacts on
	// whitespace alone, with no misleading marker.
	raw := "{\n  \"a\": \"one\",\n  \"b\": \"two\",\n  \"c\": [1, 2, 3]\n}"
	res := Finalize(raw, CompactJSON(raw))
	if !res.Applied {
		t.Fatal("pretty-printed JSON not compacted")
	}
	if strings.Contains(res.Output, "[julius]") {
		t.Errorf("marker emitted with nothing removed: %s", res.Output)
	}
	if len(res.Output) >= len(raw) {
		t.Error("whitespace compaction did not shrink output")
	}
}

func TestCompactJSONAlreadyCompactUntouched(t *testing.T) {
	raw := `{"b":"two","a":"one"}`
	res := Finalize(raw, CompactJSON(raw))
	if res.Output != raw {
		t.Errorf("already-compact JSON churned for no size win: %q", res.Output)
	}
}
