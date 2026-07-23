package session

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCacheRoundTripAndIsolation(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())

	a := Open("session-a")
	b := Open("session-b")
	a.Store("read:/x.go", []byte("hello"))

	if got, ok := a.Load("read:/x.go"); !ok || string(got) != "hello" {
		t.Errorf("Load = %q, %v", got, ok)
	}
	if _, ok := b.Load("read:/x.go"); ok {
		t.Error("cross-session cache hit — sessions must be isolated")
	}
	if _, ok := a.Load("other-key"); ok {
		t.Error("unexpected hit for unknown key")
	}
}

func TestScopedCachesAreIsolated(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	const sid = "session-x"

	parent := Open(sid)
	agent := OpenScoped(sid, "a1b52df8a75f48edb")
	sibling := OpenScoped(sid, "ac0377d08cbbb380a")
	parent.Store("read:/x.go", []byte("hello"))

	if _, ok := agent.Load("read:/x.go"); ok {
		t.Error("subagent scope must not see the parent's entries")
	}
	agent.Store("read:/x.go", []byte("hello"))
	if got, ok := agent.Load("read:/x.go"); !ok || string(got) != "hello" {
		t.Errorf("subagent scope must dedup within itself: %q, %v", got, ok)
	}
	if _, ok := sibling.Load("read:/x.go"); ok {
		t.Error("sibling subagent must not see another agent's entries")
	}
	// stability: reopening the same context finds its own entries
	if _, ok := OpenScoped(sid, "a1b52df8a75f48edb").Load("read:/x.go"); !ok {
		t.Error("reopened agent scope must find its own entries")
	}
	if OpenScoped("", "a1b52df8a75f48edb") != nil {
		t.Error("empty session must yield nil cache even with an agent id")
	}
	if OpenScoped(sid, "") == nil || filepath.Base(OpenScoped(sid, "").dir) != filepath.Base(parent.dir) {
		t.Error("empty agent id must be the main-context cache")
	}
}

// The dot separator is the namespace boundary: sanitize never emits '.',
// so no raw session id — however crafted — can name an agent-context
// directory. Regression for the flat "sid-hash" layout, where a raw
// session id shaped like another session's derived scope collided.
func TestScopedCacheNamespaceInjection(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	const sid, agentID = "victim-session", "a877c50488d25a006"

	agent := OpenScoped(sid, agentID)
	agent.Store("read:/x.go", []byte("secret"))

	derived := filepath.Base(agent.dir)
	if !strings.Contains(derived, ".") {
		t.Fatalf("derived dir %q must carry the dot separator", derived)
	}
	// Attack strings: the derived name itself, its pre-sanitize dot form,
	// and its underscore twin. None may open the agent's directory.
	for _, spoof := range []string{
		derived,
		strings.ReplaceAll(derived, ".", "_"),
		sid + "-" + strings.SplitN(derived, ".", 2)[1],
	} {
		if _, ok := Open(spoof).Load("read:/x.go"); ok {
			t.Errorf("raw session id %q reached another context's cache", spoof)
		}
	}
}

// Session id length and charset are not contractual. Discriminators are
// appended after sanitize's cap, so no length can truncate them away, and
// the hash covers the raw session id, so sessions whose sanitized names
// collide still get distinct agent scopes.
func TestScopedCacheOverlongSessions(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())

	for _, n := range []int{55, 63, 64, 65, 128} {
		sid := strings.Repeat("s", n)
		parent, agentA, agentB := Open(sid), OpenScoped(sid, "a877c50488d25a006"), OpenScoped(sid, "ac0377d08cbbb380a")
		if agentA.dir == parent.dir || agentB.dir == parent.dir {
			t.Errorf("len=%d: subagent dir collides with parent dir", n)
		}
		if agentA.dir == agentB.dir {
			t.Errorf("len=%d: sibling subagent dirs collide", n)
		}
	}
	// Overlong session ids sharing a sanitized prefix share the parent dir
	// (pre-existing cap behavior) but must not share agent scopes: the
	// discriminator hashes the raw session id, not the truncated name.
	a := OpenScoped(strings.Repeat("s", 70)+"x", "a877c50488d25a006")
	b := OpenScoped(strings.Repeat("s", 70)+"y", "a877c50488d25a006")
	if a.dir == b.dir {
		t.Errorf("same agent id across colliding sessions must not share a scope: %q", a.dir)
	}
}

// The discriminator format is load-bearing: 16 hex chars (64-bit) after
// one dot. A narrower hash would make sibling collisions plausible in
// large fleets; pin the width so it cannot regress silently.
func TestScopedCacheDiscriminatorFormat(t *testing.T) {
	c := OpenScoped("0908b841-b5b9-4d41-b211-1398ef90d195", "a877c50488d25a006")
	name := filepath.Base(c.dir)
	got := regexp.MustCompile(`^0908b841-b5b9-4d41-b211-1398ef90d195\.[0-9a-f]{16}$`).MatchString(name)
	if !got {
		t.Errorf("derived dir %q must be <sanitized-sid>.<16 hex>", name)
	}
}

func TestNilCacheIsSafe(t *testing.T) {
	var c *Cache
	c.Store("k", []byte("v"))
	if _, ok := c.Load("k"); ok {
		t.Error("nil cache must miss")
	}
	if Open("") != nil {
		t.Error("empty session ID must yield nil cache")
	}
}

func TestOversizedEntryClearsKey(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	c.Store("k", []byte("small"))
	c.Store("k", make([]byte, maxEntryBytes+1))
	if _, ok := c.Load("k"); ok {
		t.Error("oversized store must clear the key, not keep stale content")
	}
}

func TestPurgeOld(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JULIUS_SESSION_DIR", root)
	Open("fresh").Store("k", []byte("x"))
	Open("stale").Store("k", []byte("x"))
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "stale"), old, old); err != nil {
		t.Fatal(err)
	}
	PurgeOld()
	if _, ok := Open("stale").Load("k"); ok {
		t.Error("stale session must be purged")
	}
	if _, ok := Open("fresh").Load("k"); !ok {
		t.Error("fresh session must survive purge")
	}
}

func TestEntryRoundTrip(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	ts := time.Date(2026, 7, 20, 12, 30, 45, 0, time.UTC)
	cases := []struct {
		name string
		e    Entry
	}{
		{"verbatim", Entry{Content: []byte("plain text"), Form: FormVerbatim, ToolUseID: "toolu_01", Time: ts}},
		{"filtered", Entry{Content: []byte("line1\nline2\n\nline4"), Form: FormFiltered, ToolUseID: "toolu_02", Time: ts}},
		{"empty-tool-use-id", Entry{Content: []byte("m"), Form: FormFiltered, Time: ts}},
		{"diff-binary", Entry{Content: []byte{0x00, 0xff, '\n', '\t', 'j', 'u', 'l', 'i', 'u', 's', '1'}, Form: FormDiff, ToolUseID: "toolu_03", StashPath: "/tmp/raw/x.log", Time: ts}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "k:" + tc.name
			c.Commit(key, tc.e)
			// read back through the choke point: Prev carries the decoded entry
			got := c.Decide(key, []byte("something else entirely"), "").Prev
			if !bytes.Equal(got.Content, tc.e.Content) {
				t.Errorf("Content = %q, want %q", got.Content, tc.e.Content)
			}
			if got.Form != tc.e.Form {
				t.Errorf("Form = %d, want %d", got.Form, tc.e.Form)
			}
			if got.ToolUseID != tc.e.ToolUseID {
				t.Errorf("ToolUseID = %q, want %q", got.ToolUseID, tc.e.ToolUseID)
			}
			if got.StashPath != tc.e.StashPath {
				t.Errorf("StashPath = %q, want %q", got.StashPath, tc.e.StashPath)
			}
			if !got.Time.Equal(tc.e.Time) {
				t.Errorf("Time = %v, want %v", got.Time, tc.e.Time)
			}
		})
	}
}

func TestPreScopingEntryDecodesAsUnknown(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	// A julius1 entry may have been written by a subagent into the shared
	// session scope, so even a pristine verbatim header cannot attest
	// same-context provenance — it must load as FormUnknown.
	c.Store("k", []byte("julius1\t"+`{"form":"verbatim","tool_use_id":"toolu_old"}`+"\nold bytes"))

	d := c.Decide("k", []byte("old bytes"), "toolu_new")
	if d.Verdict != VerdictPass || d.SameEvent {
		t.Errorf("pre-scoping entry must yield plain VerdictPass, got %+v", d)
	}
	if d.Prev.Form != FormUnknown {
		t.Errorf("pre-scoping entry must decode as FormUnknown, got %d", d.Prev.Form)
	}
}

func TestLegacyEntryDecodesAsUnknown(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	c.Store("k", []byte("legacy content"))

	// identical content must still pass: unknown provenance never suppresses
	d := c.Decide("k", []byte("legacy content"), "toolu_01")
	if d.Verdict != VerdictPass || d.SameEvent {
		t.Errorf("legacy entry must yield plain VerdictPass, got %+v", d)
	}
	if d.Prev.Form != FormUnknown || string(d.Prev.Content) != "legacy content" {
		t.Errorf("legacy entry decoded wrong: %+v", d.Prev)
	}

	// the next Commit upgrades the entry in place
	c.Commit("k", Entry{Content: []byte("legacy content"), Form: FormVerbatim, ToolUseID: "toolu_01", Time: time.Now()})
	if d := c.Decide("k", []byte("legacy content"), "toolu_02"); d.Verdict != VerdictSuppress {
		t.Errorf("upgraded entry must suppress, got %+v", d)
	}
}

func TestDecideLegacyEntryChangedContentPasses(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	c.Store("k", []byte("old legacy content"))

	// Changed content against an unknown-provenance referent must neither
	// suppress nor diff — full passthrough is the only honest option.
	d := c.Decide("k", []byte("completely different content"), "toolu_01")
	if d.Verdict != VerdictPass || d.SameEvent {
		t.Errorf("FormUnknown prev with changed content must yield plain VerdictPass, got %+v", d)
	}
	if d.Prev.Form != FormUnknown {
		t.Errorf("legacy entry must decode as FormUnknown, got %d", d.Prev.Form)
	}
}

func TestDecideSameEventDifferentContent(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	c.Commit("k", Entry{Content: []byte("content A"), Form: FormVerbatim, ToolUseID: "toolu_x", Time: time.Now()})

	// The SameEvent check precedes any content comparison: a duplicate
	// invocation of the same event is silent even if the payload differs.
	d := c.Decide("k", []byte("content B"), "toolu_x")
	if !d.SameEvent {
		t.Errorf("same tool_use_id must report SameEvent regardless of content, got %+v", d)
	}
	if d.Verdict != VerdictPass {
		t.Errorf("SameEvent must carry VerdictPass, got %+v", d)
	}
}

func TestUndecodableEntriesDecodeAsUnknown(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	cases := []struct {
		name string
		raw  string
	}{
		{"corrupt-header", entryMagic + `{"form":"verbatim",` + "\ncontent bytes"},
		{"unknown-form", entryMagic + `{"form":"hologram","tool_use_id":"toolu_01"}` + "\ncontent bytes"},
		{"no-newline", entryMagic + `{"form":"verbatim"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "k:" + tc.name
			c.Store(key, []byte(tc.raw))

			// Undecodable entries load as FormUnknown with the whole blob as
			// content, and can never justify suppression or a diff — not
			// against their embedded payload, not against the whole blob.
			for _, content := range [][]byte{[]byte("content bytes"), []byte(tc.raw)} {
				d := c.Decide(key, content, "toolu_99")
				if d.Verdict != VerdictPass || d.SameEvent {
					t.Errorf("undecodable entry must yield plain VerdictPass for %q, got %+v", content, d)
				}
				if d.Prev.Form != FormUnknown {
					t.Errorf("undecodable entry must decode as FormUnknown, got %d", d.Prev.Form)
				}
				if !bytes.Equal(d.Prev.Content, []byte(tc.raw)) {
					t.Errorf("undecodable entry content must be the whole blob, got %q", d.Prev.Content)
				}
			}
		})
	}
}

func TestCommitOversizedRawContentClearsKey(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")

	c.Commit("k", Entry{Content: []byte("small"), Form: FormVerbatim, ToolUseID: "toolu_01", Time: time.Now()})
	c.Commit("k", Entry{Content: make([]byte, maxEntryBytes+1), Form: FormVerbatim, ToolUseID: "toolu_02", Time: time.Now()})
	if _, ok := c.Load("k"); ok {
		t.Error("oversized commit must clear the key, not keep stale content")
	}

	// Boundary: raw content exactly at the cap. The old encoded-size check
	// would have cleared this (the header pushes the blob over 1MB); the
	// raw-size check must keep it and it must dedup like any other entry.
	content := bytes.Repeat([]byte("x"), maxEntryBytes)
	c.Commit("k", Entry{Content: content, Form: FormVerbatim, ToolUseID: "toolu_03", Time: time.Now()})
	d := c.Decide("k", content, "toolu_04")
	if d.Verdict != VerdictSuppress {
		t.Fatalf("boundary-size entry must be retained and suppress, got %+v", d.Verdict)
	}
	if !bytes.Equal(d.Prev.Content, content) {
		t.Errorf("boundary-size entry content corrupted: %d bytes", len(d.Prev.Content))
	}
}

func TestCommitSameToolUseIDNoOp(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")
	c.Commit("k", Entry{Content: []byte("v1"), Form: FormVerbatim, ToolUseID: "toolu_x", Time: time.Now()})
	c.Commit("k", Entry{Content: []byte("v2"), Form: FormFiltered, ToolUseID: "toolu_x", Time: time.Now()})

	if got := c.Decide("k", nil, "").Prev; string(got.Content) != "v1" || got.Form != FormVerbatim {
		t.Errorf("same-ToolUseID commit must not overwrite, got %+v", got)
	}

	// a different tool_use_id does overwrite
	c.Commit("k", Entry{Content: []byte("v3"), Form: FormVerbatim, ToolUseID: "toolu_y", Time: time.Now()})
	if got := c.Decide("k", nil, "").Prev; string(got.Content) != "v3" {
		t.Errorf("new-ToolUseID commit must overwrite, got %+v", got)
	}

	// empty tool_use_id always writes (legacy payloads)
	c.Commit("k", Entry{Content: []byte("v4"), Form: FormVerbatim, Time: time.Now()})
	if got := c.Decide("k", nil, "").Prev; string(got.Content) != "v4" {
		t.Errorf("empty-ToolUseID commit must overwrite, got %+v", got)
	}
}

func TestDecideNeverWrites(t *testing.T) {
	t.Setenv("JULIUS_SESSION_DIR", t.TempDir())
	c := Open("s")

	_ = c.Decide("k", []byte("content"), "toolu_01")
	if _, ok := c.Load("k"); ok {
		t.Error("Decide on a missing key must not create an entry")
	}

	c.Commit("k", Entry{Content: []byte("v1"), Form: FormVerbatim, ToolUseID: "toolu_01", Time: time.Now()})
	before, _ := c.Load("k")
	_ = c.Decide("k", []byte("different"), "toolu_02")
	after, _ := c.Load("k")
	if !bytes.Equal(before, after) {
		t.Error("Decide must never modify the stored entry")
	}

	// nil-cache safety mirrors Load/Store
	var nc *Cache
	nc.Commit("k", Entry{Content: []byte("x"), Form: FormVerbatim})
	if d := nc.Decide("k", []byte("x"), "id"); d.Verdict != VerdictPass || d.SameEvent {
		t.Errorf("nil cache must yield plain VerdictPass, got %+v", d)
	}
}

func TestDiff(t *testing.T) {
	oldText := "a\nb\nc\nd"
	newText := "a\nB\nc\nd\ne"
	d, ok := Diff(oldText, newText)
	if !ok {
		t.Fatal("diff failed")
	}
	for _, want := range []string{"- b", "+ B", "+ e"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	if strings.Contains(d, "- a") || strings.Contains(d, "- c") {
		t.Errorf("diff touched unchanged lines:\n%s", d)
	}

	if _, ok := Diff(strings.Repeat("x\n", 3000), strings.Repeat("y\n", 3000)); ok {
		t.Error("oversized diff must report failure")
	}
}
