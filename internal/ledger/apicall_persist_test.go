package ledger

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConcurrentAPICallsOnSharedConn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.RecordAPICall(APICall{AppTag: "a", Provider: "anthropic", Model: "m", Input: 10, Output: 5}); err != nil {
				t.Errorf("concurrent write: %v", err)
			}
		}()
	}
	wg.Wait()
	tot, err := l.APIUsage(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if tot.Calls != 20 {
		t.Errorf("recorded %d, want 20 — concurrent writes on the shared connection are being dropped", tot.Calls)
	}
}

func TestSequentialAPICallsPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l.db")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := l.RecordAPICall(APICall{AppTag: "a", Provider: "anthropic", Model: "m", Input: 10, Output: 5}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	l.Close()

	l2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	tot, err := l2.APIUsage(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if tot.Calls != 5 {
		t.Errorf("recorded %d, want 5", tot.Calls)
	}
}
