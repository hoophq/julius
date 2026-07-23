package session

import (
	"bytes"
	"encoding/json"
	"os"
	"time"
)

// Form records how a cached output actually entered the agent's context.
// Suppression is only honest against FormVerbatim referents: anything else
// means the agent never saw the raw bytes, so a "see above" marker would lie.
type Form uint8

const (
	// FormUnknown marks a legacy or undecodable entry: provenance unknown,
	// so it can never justify suppression.
	FormUnknown Form = iota
	FormVerbatim
	FormFiltered
	FormDiff
)

// Entry is one cached observation: what the tool produced plus how (and
// under which hook event) it was delivered to the agent.
type Entry struct {
	Content   []byte
	Form      Form
	ToolUseID string
	StashPath string
	Time      time.Time
}

// Verdict is Decide's recommendation for fresh content.
type Verdict uint8

const (
	VerdictPass Verdict = iota
	VerdictSuppress
	VerdictDiff
)

// Decision carries the verdict plus the previous entry for marker text.
// SameEvent means this is a duplicate hook invocation of the very event
// that produced the stored entry — the caller must emit nothing.
type Decision struct {
	Verdict   Verdict
	SameEvent bool
	Prev      Entry
}

// Decide reports how content relates to what the session already saw under
// key. Read-only by contract: recording the outcome is Commit's job, so a
// duplicate hook invocation can never self-match against its own twin.
func (c *Cache) Decide(key string, content []byte, toolUseID string) Decision {
	if c == nil {
		return Decision{Verdict: VerdictPass}
	}
	data, ok := c.Load(key)
	if !ok {
		return Decision{Verdict: VerdictPass}
	}
	prev := decodeEntry(data)
	if prev.Form == FormUnknown {
		return Decision{Verdict: VerdictPass, Prev: prev}
	}
	if toolUseID != "" && prev.ToolUseID == toolUseID {
		return Decision{Verdict: VerdictPass, SameEvent: true, Prev: prev}
	}
	if prev.Form != FormVerbatim {
		return Decision{Verdict: VerdictPass, Prev: prev}
	}
	if bytes.Equal(prev.Content, content) {
		return Decision{Verdict: VerdictSuppress, Prev: prev}
	}
	return Decision{Verdict: VerdictDiff, Prev: prev}
}

// Commit stores e under key via the atomic write path. If the stored entry
// already carries the same non-empty ToolUseID, this is a no-op: under the
// duplicate-invocation race the first writer's record stands.
//
// The size cap applies to the raw content, not the encoded blob — the
// header must never shrink the effective cap below the legacy Store(raw)
// semantics. Oversized content clears the key so a stale version can never
// be suppressed or diffed against.
func (c *Cache) Commit(key string, e Entry) {
	if c == nil {
		return
	}
	if e.ToolUseID != "" {
		if data, ok := c.Load(key); ok && decodeEntry(data).ToolUseID == e.ToolUseID {
			return
		}
	}
	if len(e.Content) > maxEntryBytes {
		_ = os.Remove(c.path(key))
		return
	}
	c.write(key, encodeEntry(e))
}

// On-disk entry format: magic + one-line JSON header + "\n" + raw content
// bytes. The JSON header takes new fields without a format bump; anything
// that fails to decode loads as FormUnknown so legacy entries can never
// justify suppression, and the next Commit upgrades them in place.
//
// The magic bumps only when older entries can no longer attest what
// suppression requires. julius1 entries predate per-agent scoping: a
// subagent could have written them into the shared session scope, so they
// cannot attest same-context provenance and must load as FormUnknown.
const entryMagic = "julius2\t"

type entryHeader struct {
	Form      string `json:"form"`
	ToolUseID string `json:"tool_use_id"`
	Stash     string `json:"stash"`
	TS        string `json:"ts"`
}

var formNames = map[Form]string{
	FormVerbatim: "verbatim",
	FormFiltered: "filtered",
	FormDiff:     "diff",
}

var formsByName = map[string]Form{
	"verbatim": FormVerbatim,
	"filtered": FormFiltered,
	"diff":     FormDiff,
}

func encodeEntry(e Entry) []byte {
	h, _ := json.Marshal(entryHeader{
		Form:      formNames[e.Form],
		ToolUseID: e.ToolUseID,
		Stash:     e.StashPath,
		TS:        e.Time.UTC().Format(time.RFC3339),
	})
	buf := make([]byte, 0, len(entryMagic)+len(h)+1+len(e.Content))
	buf = append(buf, entryMagic...)
	buf = append(buf, h...)
	buf = append(buf, '\n')
	return append(buf, e.Content...)
}

func decodeEntry(data []byte) Entry {
	if !bytes.HasPrefix(data, []byte(entryMagic)) {
		return Entry{Content: data, Form: FormUnknown}
	}
	nl := bytes.IndexByte(data, '\n')
	if nl < 0 {
		return Entry{Content: data, Form: FormUnknown}
	}
	var h entryHeader
	if err := json.Unmarshal(data[len(entryMagic):nl], &h); err != nil {
		return Entry{Content: data, Form: FormUnknown}
	}
	form, ok := formsByName[h.Form]
	if !ok {
		return Entry{Content: data, Form: FormUnknown}
	}
	t, _ := time.Parse(time.RFC3339, h.TS)
	return Entry{Content: data[nl+1:], Form: form, ToolUseID: h.ToolUseID, StashPath: h.Stash, Time: t}
}
