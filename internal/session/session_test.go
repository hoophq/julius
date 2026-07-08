package session

import (
	"os"
	"path/filepath"
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
