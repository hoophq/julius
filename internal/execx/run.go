// Package execx runs wrapped commands and preserves their raw output for
// recovery. julius wraps commands the agent runs; correctness here means
// faithful exit codes, no orphan children, and never losing failure detail.
package execx

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

// Outcome captures a wrapped command's execution result.
type Outcome struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes argv[0] with argv[1:], capturing output. Stdin is inherited.
// SIGINT/SIGTERM are forwarded to the child so nothing is orphaned.
func Run(argv []string) (Outcome, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = os.Stdin

	if err := cmd.Start(); err != nil {
		return Outcome{ExitCode: 127}, err
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = cmd.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()

	err := cmd.Wait()
	close(done)
	signal.Stop(sigs)

	out := Outcome{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			out.ExitCode = ee.ExitCode()
			return out, nil
		}
		out.ExitCode = 127
		return out, err
	}
	return out, nil
}
