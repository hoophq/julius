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
