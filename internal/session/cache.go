// Package session remembers what a coding-agent session has already seen,
// so repeated identical tool outputs can collapse to a marker and changed
// files can be delivered as diffs.
//
// Safety model: the cache never substitutes for fresh data — callers always
// hold the fresh output and only use the cache to decide how much of it to
// forward. Keys are scoped per agent context (session ID, plus an agent
// discriminator for subagent events — see ScopeID), so one context can
// never dedup against another's history.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"
)

const (
	// maxEntryBytes bounds a single cached content blob.
	maxEntryBytes = 1 << 20 // 1MB
	// purgeAfter is how long a session directory survives after its last write.
	purgeAfter = 7 * 24 * time.Hour
	// maxScopeRunes caps a scope directory name; ScopeID must keep its
	// agent discriminator inside this window or contexts would merge.
	maxScopeRunes = 64
)

// Cache is a per-session content store on disk.
type Cache struct {
	dir string
}

// Root returns the base directory holding all session caches.
func Root() string {
	if dir := os.Getenv("JULIUS_SESSION_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "julius", "sessions")
	}
	return filepath.Join(home, ".local", "share", "julius", "sessions")
}

// Open returns the cache for a session, creating its directory lazily.
// An empty session ID yields a nil cache; all methods on a nil Cache are
// safe no-ops, so callers never need to branch.
func Open(sessionID string) *Cache {
	if sessionID == "" {
		return nil
	}
	return &Cache{dir: filepath.Join(Root(), sanitize(sessionID))}
}

// Load returns the previously stored content for key, if any.
func (c *Cache) Load(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil {
		return nil, false
	}
	return data, true
}

// Store saves content under key (atomic write). Oversized content clears
// the key instead so a stale version can never be diffed against.
func (c *Cache) Store(key string, content []byte) {
	if c == nil {
		return
	}
	if len(content) > maxEntryBytes {
		_ = os.Remove(c.path(key))
		return
	}
	c.write(key, content)
}

// write persists content under key atomically, with no size gate: Commit
// caps the raw content before encoding, so an encoded blob may exceed
// maxEntryBytes by the small header overhead.
func (c *Cache) write(key string, content []byte) {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	tmp := c.path(key) + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path(key))
}

func (c *Cache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:16]))
}

// PurgeOld removes session directories idle for longer than purgeAfter.
// Best-effort and cheap; call opportunistically.
func PurgeOld() {
	entries, err := os.ReadDir(Root())
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-purgeAfter)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(Root(), e.Name()))
		}
	}
}

func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
		if len(out) >= maxScopeRunes {
			break
		}
	}
	return string(out)
}
