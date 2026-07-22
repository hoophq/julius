// Package ledger persists savings and usage records.
//
// Three tables, three truths — kept separate on purpose and never blended:
//
//	hook_events   — command-surface savings, token counts are ESTIMATES
//	api_calls     — proxy-surface usage, token counts are provider-reported
//	proxy_savings — proxy request compression, token counts are ESTIMATES
//
// Writes come from short-lived concurrent processes (hooks, wrappers), so
// the database runs in WAL mode with a busy timeout generous enough for
// slow filesystems; writes happen after command output is already
// delivered, so waiting costs nothing on the command path. Recording is
// best-effort everywhere: analytics must never break a command.
package ledger

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// HookEvent is one command-surface savings record.
type HookEvent struct {
	TS           time.Time
	SessionID    string
	Kind         string // "command" (wrapper) | "rewrite" | "post_compress"
	Tool         string // originating tool, e.g. "Bash"
	Command      string
	TokensBefore int
	TokensAfter  int
	RawPath      string
}

// Ledger wraps the SQLite database.
type Ledger struct {
	db *sql.DB
}

// DefaultPath returns the ledger location, honoring JULIUS_LEDGER for tests.
func DefaultPath() string {
	if p := os.Getenv("JULIUS_LEDGER"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "julius", "ledger.db")
	}
	return filepath.Join(home, ".local", "share", "julius", "ledger.db")
}

const schema = `
CREATE TABLE IF NOT EXISTS hook_events (
  id INTEGER PRIMARY KEY,
  ts TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  tool TEXT NOT NULL DEFAULT '',
  command TEXT NOT NULL,
  tokens_before INTEGER NOT NULL,
  tokens_after INTEGER NOT NULL,
  raw_path TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS hook_events_ts ON hook_events(ts);
CREATE TABLE IF NOT EXISTS api_calls (
  id INTEGER PRIMARY KEY,
  ts TEXT NOT NULL,
  app_tag TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  input_tokens INTEGER NOT NULL,
  output_tokens INTEGER NOT NULL,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_write_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS api_calls_ts ON api_calls(ts);
CREATE TABLE IF NOT EXISTS proxy_savings (
  id INTEGER PRIMARY KEY,
  ts TEXT NOT NULL,
  app_tag TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL,
  tokens_before INTEGER NOT NULL,
  tokens_after INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS proxy_savings_ts ON proxy_savings(ts);
`

// Open opens (creating if needed) the ledger at path.
func Open(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(2000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	if err := busyRetry(func() error { _, e := db.Exec(schema); return e }); err != nil {
		db.Close()
		return nil, err
	}
	return &Ledger{db: db}, nil
}

// busyRetry runs f, retrying while SQLite reports the database locked.
// SQLite bypasses the busy handler (and with it the busy_timeout) in
// several lock states, so concurrent short-lived processes can see
// immediate SQLITE_BUSY regardless of the timeout — observed on Windows
// under schema-creation races. A bounded application-level retry is the
// only reliable cross-platform behavior.
func busyRetry(f func() error) error {
	delay := 5 * time.Millisecond
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := f()
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
}

// isBusy matches SQLITE_BUSY (5) and SQLITE_LOCKED (6), typed when the
// driver surfaces its error type and by message otherwise.
func isBusy(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == 5 || se.Code() == 6
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLITE_BUSY") || strings.Contains(msg, "SQLITE_LOCKED")
}

// Close releases the database handle.
func (l *Ledger) Close() error { return l.db.Close() }

// RecordHookEvent inserts one savings record.
func (l *Ledger) RecordHookEvent(ev HookEvent) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	return busyRetry(func() error {
		_, err := l.db.Exec(
			`INSERT INTO hook_events (ts, session_id, kind, tool, command, tokens_before, tokens_after, raw_path)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.TS.UTC().Format(time.RFC3339), ev.SessionID, ev.Kind, ev.Tool, ev.Command,
			ev.TokensBefore, ev.TokensAfter, ev.RawPath,
		)
		return err
	})
}

// APICall is one proxy-surface usage record: exact, provider-reported.
type APICall struct {
	TS         time.Time
	AppTag     string
	Provider   string
	Model      string
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
}

// RecordAPICall inserts one usage record.
func (l *Ledger) RecordAPICall(c APICall) error {
	if c.TS.IsZero() {
		c.TS = time.Now()
	}
	return busyRetry(func() error {
		_, err := l.db.Exec(
			`INSERT INTO api_calls (ts, app_tag, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.TS.UTC().Format(time.RFC3339), c.AppTag, c.Provider, c.Model,
			c.Input, c.Output, c.CacheRead, c.CacheWrite,
		)
		return err
	})
}

// APITotals is an aggregate over api_calls rows.
type APITotals struct {
	Calls      int
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
}

// APIUsage aggregates the proxy surface since a point in time.
func (l *Ledger) APIUsage(since time.Time) (APITotals, error) {
	var t APITotals
	err := l.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0)
		 FROM api_calls WHERE ts >= ?`,
		since.UTC().Format(time.RFC3339),
	).Scan(&t.Calls, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite)
	return t, err
}

// AppUsage is a per-app/model aggregate row.
type AppUsage struct {
	AppTag   string
	Provider string
	Model    string
	APITotals
}

// APIUsageByApp breaks down proxy usage per app tag and model.
func (l *Ledger) APIUsageByApp(since time.Time, limit int) ([]AppUsage, error) {
	rows, err := l.db.Query(
		`SELECT app_tag, provider, model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0)
		 FROM api_calls WHERE ts >= ?
		 GROUP BY app_tag, provider, model
		 ORDER BY SUM(input_tokens) + SUM(output_tokens) DESC
		 LIMIT ?`,
		since.UTC().Format(time.RFC3339), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppUsage
	for rows.Next() {
		var a AppUsage
		if err := rows.Scan(&a.AppTag, &a.Provider, &a.Model, &a.Calls, &a.Input, &a.Output, &a.CacheRead, &a.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ModelUsage is a per-provider/model aggregate row.
type ModelUsage struct {
	Provider string
	Model    string
	APITotals
}

// APIUsageByModel aggregates the proxy surface per provider and model,
// with no row limit: cost totals must cover every row, not a top-N.
func (l *Ledger) APIUsageByModel(since time.Time) ([]ModelUsage, error) {
	rows, err := l.db.Query(
		`SELECT provider, model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0)
		 FROM api_calls WHERE ts >= ?
		 GROUP BY provider, model`,
		since.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelUsage
	for rows.Next() {
		var m ModelUsage
		if err := rows.Scan(&m.Provider, &m.Model, &m.Calls, &m.Input, &m.Output, &m.CacheRead, &m.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ProxySaving is one request-compression record: estimated tokens shaved
// off a request body before it reached the provider. Estimates — reported
// separately from the exact api_calls numbers, always.
type ProxySaving struct {
	TS           time.Time
	AppTag       string
	Provider     string
	TokensBefore int
	TokensAfter  int
}

// RecordProxySaving inserts one request-compression record.
func (l *Ledger) RecordProxySaving(p ProxySaving) error {
	if p.TS.IsZero() {
		p.TS = time.Now()
	}
	return busyRetry(func() error {
		_, err := l.db.Exec(
			`INSERT INTO proxy_savings (ts, app_tag, provider, tokens_before, tokens_after)
			 VALUES (?, ?, ?, ?, ?)`,
			p.TS.UTC().Format(time.RFC3339), p.AppTag, p.Provider, p.TokensBefore, p.TokensAfter,
		)
		return err
	})
}

// ProxySavingsTotals aggregates proxy_savings since a point in time.
func (l *Ledger) ProxySavingsTotals(since time.Time) (Totals, error) {
	var t Totals
	err := l.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM proxy_savings WHERE ts >= ?`,
		since.UTC().Format(time.RFC3339),
	).Scan(&t.Events, &t.TokensBefore, &t.TokensAfter)
	return t, err
}

// Totals is an aggregate over an estimate-based savings table.
type Totals struct {
	Events       int
	TokensBefore int
	TokensAfter  int
}

// Saved returns estimated tokens saved.
func (t Totals) Saved() int { return t.TokensBefore - t.TokensAfter }

// HookTotals aggregates hook_events since a point in time.
func (l *Ledger) HookTotals(since time.Time) (Totals, error) {
	var t Totals
	err := l.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM hook_events WHERE ts >= ?`,
		since.UTC().Format(time.RFC3339),
	).Scan(&t.Events, &t.TokensBefore, &t.TokensAfter)
	return t, err
}

// KindTotals is a per-kind aggregate row.
type KindTotals struct {
	Kind string
	Totals
}

// HookKindTotals aggregates hook_events per kind since a point in time.
// A non-empty sessionID restricts to that session's rows. Kinds are
// returned as recorded — unknown kinds are the caller's to disclose, not
// to fold into a known bucket.
func (l *Ledger) HookKindTotals(since time.Time, sessionID string) ([]KindTotals, error) {
	rows, err := l.db.Query(
		`SELECT kind, COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM hook_events WHERE ts >= ? AND (? = '' OR session_id = ?)
		 GROUP BY kind`,
		since.UTC().Format(time.RFC3339), sessionID, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KindTotals
	for rows.Next() {
		var k KindTotals
		if err := rows.Scan(&k.Kind, &k.Events, &k.TokensBefore, &k.TokensAfter); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ToolTotals is a per-tool aggregate row.
type ToolTotals struct {
	Tool string
	Totals
}

// HookToolTotals aggregates one kind's hook_events per originating tool.
// Rows recorded before tool attribution existed come back with an empty
// Tool — the caller reports them as unattributed, never guesses.
func (l *Ledger) HookToolTotals(since time.Time, kind, sessionID string) ([]ToolTotals, error) {
	rows, err := l.db.Query(
		`SELECT tool, COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM hook_events WHERE ts >= ? AND kind = ? AND (? = '' OR session_id = ?)
		 GROUP BY tool
		 ORDER BY SUM(tokens_before) - SUM(tokens_after) DESC`,
		since.UTC().Format(time.RFC3339), kind, sessionID, sessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ToolTotals
	for rows.Next() {
		var tt ToolTotals
		if err := rows.Scan(&tt.Tool, &tt.Events, &tt.TokensBefore, &tt.TokensAfter); err != nil {
			return nil, err
		}
		out = append(out, tt)
	}
	return out, rows.Err()
}

// HookNoSessionTotals aggregates rows carrying no session attribution.
// Session-scoped views disclose these as excluded — they may or may not
// belong to the session being asked about, and guessing is not an option.
func (l *Ledger) HookNoSessionTotals(since time.Time) (Totals, error) {
	var t Totals
	err := l.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM hook_events WHERE ts >= ? AND session_id = ''`,
		since.UTC().Format(time.RFC3339),
	).Scan(&t.Events, &t.TokensBefore, &t.TokensAfter)
	return t, err
}

// CommandTotals is a per-command aggregate row.
type CommandTotals struct {
	Command string
	Totals
}

// TopCommands returns the highest-saving commands since a point in time.
// Only command-surface kinds qualify: native-tool and dedup rows carry
// pseudo-commands ("read /path", "grep pattern") that are not commands.
func (l *Ledger) TopCommands(since time.Time, sessionID string, limit int) ([]CommandTotals, error) {
	rows, err := l.db.Query(
		`SELECT command, COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM hook_events WHERE ts >= ? AND kind IN ('command', 'rewrite') AND (? = '' OR session_id = ?)
		 GROUP BY command
		 ORDER BY SUM(tokens_before) - SUM(tokens_after) DESC
		 LIMIT ?`,
		since.UTC().Format(time.RFC3339), sessionID, sessionID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommandTotals
	for rows.Next() {
		var c CommandTotals
		if err := rows.Scan(&c.Command, &c.Events, &c.TokensBefore, &c.TokensAfter); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
