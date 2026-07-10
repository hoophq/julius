package ledger

import (
	"errors"
	"testing"
)

func TestBusyRetryRecoversFromTransientLock(t *testing.T) {
	// The message form is exactly what the driver produces when the busy
	// handler is bypassed and the error reaches the application.
	busy := errors.New("database is locked (5) (SQLITE_BUSY)")
	calls := 0
	err := busyRetry(func() error {
		calls++
		if calls < 3 {
			return busy
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("busyRetry = %v after %d calls, want nil after 3", err, calls)
	}
}

func TestBusyRetryPassesThroughOtherErrors(t *testing.T) {
	boom := errors.New("no such table: hook_events")
	calls := 0
	if err := busyRetry(func() error { calls++; return boom }); !errors.Is(err, boom) || calls != 1 {
		t.Errorf("busyRetry = %v after %d calls, want immediate passthrough", err, calls)
	}
}
