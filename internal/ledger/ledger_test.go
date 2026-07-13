package ledger

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Ledger {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestRecordAndAggregate(t *testing.T) {
	l := openTemp(t)
	base := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	events := []HookEvent{
		{TS: base, Kind: "command", Command: "git status", TokensBefore: 500, TokensAfter: 100},
		{TS: base.Add(time.Minute), Kind: "command", Command: "git status", TokensBefore: 300, TokensAfter: 60},
		{TS: base.Add(2 * time.Minute), Kind: "command", Command: "go test", TokensBefore: 2000, TokensAfter: 50},
	}
	for _, ev := range events {
		if err := l.RecordHookEvent(ev); err != nil {
			t.Fatal(err)
		}
	}

	tot, err := l.HookTotals(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if tot.Events != 3 || tot.TokensBefore != 2800 || tot.TokensAfter != 210 || tot.Saved() != 2590 {
		t.Errorf("totals = %+v", tot)
	}

	top, err := l.TopCommands(base.Add(-time.Hour), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 2 || top[0].Command != "go test" || top[1].Command != "git status" {
		t.Errorf("top = %+v", top)
	}

	// since-filter excludes older events
	tot, err = l.HookTotals(base.Add(90 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if tot.Events != 1 {
		t.Errorf("since filter: events = %d, want 1", tot.Events)
	}
}

func TestConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// each goroutine opens its own handle, like separate hook processes
			l, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			defer l.Close()
			errs <- l.RecordHookEvent(HookEvent{Kind: "command", Command: "git status", TokensBefore: 10, TokensAfter: 2})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	tot, err := l.HookTotals(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if tot.Events != 10 {
		t.Errorf("concurrent writes recorded %d events, want 10", tot.Events)
	}
}

func TestAPIUsageAggregates(t *testing.T) {
	l := openTemp(t)
	base := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	calls := []APICall{
		{TS: base, AppTag: "bot", Provider: "anthropic", Model: "claude-opus-4-8", Input: 1000, Output: 100, CacheRead: 400, CacheWrite: 50},
		{TS: base.Add(time.Minute), AppTag: "bot", Provider: "anthropic", Model: "claude-opus-4-8", Input: 500, Output: 50},
		{TS: base.Add(2 * time.Minute), AppTag: "batch", Provider: "openai", Model: "gpt-5.4", Input: 2000, Output: 200, CacheRead: 800},
	}
	for _, c := range calls {
		if err := l.RecordAPICall(c); err != nil {
			t.Fatal(err)
		}
	}

	byApp, err := l.APIUsageByApp(base.Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byApp) != 2 {
		t.Fatalf("byApp rows = %d, want 2", len(byApp))
	}
	for _, a := range byApp {
		if a.Provider == "" {
			t.Errorf("row %s/%s has empty provider", a.AppTag, a.Model)
		}
	}
	if byApp[0].AppTag != "batch" || byApp[0].Provider != "openai" {
		t.Errorf("ordering by volume: %+v", byApp[0])
	}

	byModel, err := l.APIUsageByModel(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(byModel) != 2 {
		t.Fatalf("byModel rows = %d, want 2", len(byModel))
	}
	for _, m := range byModel {
		switch m.Model {
		case "claude-opus-4-8":
			if m.Provider != "anthropic" || m.Calls != 2 || m.Input != 1500 || m.Output != 150 || m.CacheRead != 400 || m.CacheWrite != 50 {
				t.Errorf("opus aggregate = %+v", m)
			}
		case "gpt-5.4":
			if m.Provider != "openai" || m.Calls != 1 || m.Input != 2000 || m.CacheRead != 800 {
				t.Errorf("gpt aggregate = %+v", m)
			}
		default:
			t.Errorf("unexpected model %q", m.Model)
		}
	}
}

func TestProxySavings(t *testing.T) {
	l := openTemp(t)
	base := time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)
	rows := []ProxySaving{
		{TS: base, AppTag: "agent", Provider: "anthropic", TokensBefore: 900, TokensAfter: 200},
		{TS: base.Add(time.Minute), AppTag: "agent", Provider: "openai", TokensBefore: 400, TokensAfter: 100},
	}
	for _, p := range rows {
		if err := l.RecordProxySaving(p); err != nil {
			t.Fatal(err)
		}
	}

	tot, err := l.ProxySavingsTotals(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if tot.Events != 2 || tot.TokensBefore != 1300 || tot.TokensAfter != 300 || tot.Saved() != 1000 {
		t.Errorf("totals = %+v", tot)
	}

	// since-filter excludes older rows
	tot, err = l.ProxySavingsTotals(base.Add(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if tot.Events != 1 {
		t.Errorf("since filter: events = %d, want 1", tot.Events)
	}

	// proxy savings never leak into the hook surface
	hook, err := l.HookTotals(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if hook.Events != 0 {
		t.Errorf("hook surface contaminated: %+v", hook)
	}
}
