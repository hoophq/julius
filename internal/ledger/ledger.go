// Package ledger persists savings and usage records.
//
// Two tables, two truths — kept separate on purpose and never blended:
//
//	hook_events — command-surface savings, token counts are ESTIMATES
//	api_calls   — proxy-surface usage, token counts are provider-reported
//
// Writes come from short-lived concurrent processes (hooks, wrappers), so
// the database runs in WAL mode with a short busy timeout. Recording is
// best-effort everywhere: analytics must never break a command.
package ledger

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
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
`

// Open opens (creating if needed) the ledger at path.
func Open(path string) (*Ledger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(200)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Ledger{db: db}, nil
}

// Close releases the database handle.
func (l *Ledger) Close() error { return l.db.Close() }

// RecordHookEvent inserts one savings record.
func (l *Ledger) RecordHookEvent(ev HookEvent) error {
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	_, err := l.db.Exec(
		`INSERT INTO hook_events (ts, session_id, kind, tool, command, tokens_before, tokens_after, raw_path)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.TS.UTC().Format(time.RFC3339), ev.SessionID, ev.Kind, ev.Tool, ev.Command,
		ev.TokensBefore, ev.TokensAfter, ev.RawPath,
	)
	return err
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
	_, err := l.db.Exec(
		`INSERT INTO api_calls (ts, app_tag, provider, model, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		c.TS.UTC().Format(time.RFC3339), c.AppTag, c.Provider, c.Model,
		c.Input, c.Output, c.CacheRead, c.CacheWrite,
	)
	return err
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
	AppTag string
	Model  string
	APITotals
}

// APIUsageByApp breaks down proxy usage per app tag and model.
func (l *Ledger) APIUsageByApp(since time.Time, limit int) ([]AppUsage, error) {
	rows, err := l.db.Query(
		`SELECT app_tag, model, COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(output_tokens),0),
		        COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(cache_write_tokens),0)
		 FROM api_calls WHERE ts >= ?
		 GROUP BY app_tag, model
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
		if err := rows.Scan(&a.AppTag, &a.Model, &a.Calls, &a.Input, &a.Output, &a.CacheRead, &a.CacheWrite); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Totals summarizes the hook surface since the given time.
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

// CommandTotals is a per-command aggregate row.
type CommandTotals struct {
	Command string
	Totals
}

// TopCommands returns the highest-saving commands since a point in time.
func (l *Ledger) TopCommands(since time.Time, limit int) ([]CommandTotals, error) {
	rows, err := l.db.Query(
		`SELECT command, COUNT(*), COALESCE(SUM(tokens_before),0), COALESCE(SUM(tokens_after),0)
		 FROM hook_events WHERE ts >= ?
		 GROUP BY command
		 ORDER BY SUM(tokens_before) - SUM(tokens_after) DESC
		 LIMIT ?`,
		since.UTC().Format(time.RFC3339), limit,
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
