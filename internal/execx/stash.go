package execx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// stashMin is the minimum raw size worth stashing; tiny outputs are
	// cheap enough to keep in context.
	stashMin = 500
	// stashMaxFiles bounds the stash directory size via rotation.
	stashMaxFiles = 20
	// stashMaxBytes caps a single stash file.
	stashMaxBytes = 1 << 20 // 1MB
)

// StashDir returns the directory where raw outputs are kept.
func StashDir() string {
	if dir := os.Getenv("JULIUS_RAW_DIR"); dir != "" {
		return dir
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "julius", "raw")
	}
	return filepath.Join(base, ".local", "share", "julius", "raw")
}

// Stash writes raw output to disk and returns a hint line to append to the
// filtered output, e.g. "[julius] raw output: ~/.local/share/julius/raw/...".
// Returns "" when the output is too small to bother or the write fails —
// stashing is best-effort and must never break the command path.
func Stash(raw, slug string, now time.Time) string {
	if len(raw) < stashMin {
		return ""
	}
	dir := StashDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	if len(raw) > stashMaxBytes {
		raw = raw[:stashMaxBytes] + "\n[truncated at 1MB]\n"
	}
	name := fmt.Sprintf("%s-%s.log", now.UTC().Format("20060102-150405"), sanitizeSlug(slug))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		return ""
	}
	rotate(dir)
	return fmt.Sprintf("[julius] raw output: %s", path)
}

func sanitizeSlug(slug string) string {
	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= 40 {
			break
		}
	}
	return b.String()
}

func rotate(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, e.Name())
		}
	}
	if len(logs) <= stashMaxFiles {
		return
	}
	sort.Strings(logs) // names sort chronologically by construction
	for _, name := range logs[:len(logs)-stashMaxFiles] {
		_ = os.Remove(filepath.Join(dir, name))
	}
}
