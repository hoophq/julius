package filter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// jsonArrayCap bounds list payloads: agents act on the first page of
	// results, not the tail of a 200-item dump.
	jsonArrayCap = 20
	// jsonStrMax bounds leaf strings (embedded descriptions, documents).
	jsonStrMax = 500
)

// CompactJSON compresses a JSON document for agent consumption: null
// object fields are dropped, arrays are capped at jsonArrayCap items,
// and leaf strings longer than jsonStrMax runes are truncated. Keys whose
// values agents chain into later calls (ids, urls, and similar) are never
// truncated. Whatever was removed is disclosed in a trailing marker line,
// and the JSON itself stays valid.
//
// Non-JSON input passes through untouched (Applied=false). Object keys are
// re-serialized in sorted order; numbers round-trip verbatim via
// json.Number. Callers must pass the result through Finalize.
func CompactJSON(raw string) Result {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return Result{Output: raw}
	}
	switch v.(type) {
	case map[string]any, []any:
	default:
		// Bare scalars have nothing to compact.
		return Result{Output: raw}
	}

	var st compactStats
	v = compactValue(v, "", &st)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return Result{Output: raw}
	}
	out := strings.TrimRight(buf.String(), "\n")

	// Nothing removed and no whitespace win: keep the original rather than
	// churning key order for zero benefit.
	if st == (compactStats{}) && len(out) >= len(raw) {
		return Result{Output: raw}
	}

	var notes []string
	if st.nulls > 0 {
		notes = append(notes, fmt.Sprintf("%d null fields dropped", st.nulls))
	}
	if st.items > 0 {
		notes = append(notes, fmt.Sprintf("%d array items omitted", st.items))
	}
	if st.strs > 0 {
		notes = append(notes, fmt.Sprintf("%d long strings truncated", st.strs))
	}
	if len(notes) > 0 {
		out += "\n[julius] compacted JSON: " + strings.Join(notes, ", ")
	}
	return Result{Output: out, Applied: true}
}

type compactStats struct {
	nulls, items, strs int
}

func compactValue(v any, key string, st *compactStats) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			if val == nil {
				st.nulls++
				continue
			}
			m[k] = compactValue(val, k, st)
		}
		return m
	case []any:
		if len(t) > jsonArrayCap {
			st.items += len(t) - jsonArrayCap
			t = t[:jsonArrayCap]
		}
		out := make([]any, len(t))
		// Null ELEMENTS are kept: dropping them would shift positions in
		// order-significant arrays.
		for i, val := range t {
			out[i] = compactValue(val, key, st)
		}
		return out
	case string:
		if r := []rune(t); len(r) > jsonStrMax && !protectedKey(key) {
			st.strs++
			return string(r[:jsonStrMax]) + "…"
		}
		return t
	default:
		return v
	}
}

// protectedKey reports whether a field's value must survive intact because
// agents feed it back into subsequent calls — a truncated id or url breaks
// the next request, which costs far more than the tokens saved.
func protectedKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"id", "url", "href", "identifier", "slug", "sha", "key"} {
		if k == s || strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}
