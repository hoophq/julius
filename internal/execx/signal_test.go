//go:build !windows

package execx

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// TestRunForwardsSignals proves a SIGINT to julius reaches the wrapped
// child: the long-running sleep must die promptly instead of being
// orphaned.
func TestRunForwardsSignals(t *testing.T) {
	done := make(chan Outcome, 1)
	go func() {
		out, _ := Run([]string{"sleep", "30"})
		done <- out
	}()

	// Let Run start the child and register its signal handler.
	time.Sleep(300 * time.Millisecond)
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := self.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	select {
	case out := <-done:
		if out.ExitCode == 0 {
			t.Errorf("child exited 0 after SIGINT, want signal exit, got %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child still running 5s after SIGINT — signal was not forwarded")
	}
}
