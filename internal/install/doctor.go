package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hoophq/julius/internal/execx"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/ui"
)

// Check is one doctor verification result.
type Check struct {
	Name   string
	OK     bool
	Detail string
}

// Doctor verifies the julius installation end to end.
func Doctor(cwd string) []Check {
	var checks []Check

	if path, err := exec.LookPath("julius"); err == nil {
		checks = append(checks, Check{"binary on PATH", true, path})
	} else {
		checks = append(checks, Check{"binary on PATH", false, "julius not found in PATH — hooks will fail silently"})
	}

	hookFound := false
	var hookWhere string
	for _, global := range []bool{true, false} {
		if path, err := SettingsPath(global, cwd); err == nil && Installed(path) {
			hookFound = true
			hookWhere = path
			break
		}
	}
	if hookFound {
		checks = append(checks, Check{"Claude Code hook registered", true, hookWhere})
	} else {
		checks = append(checks, Check{"Claude Code hook registered", false, "run `julius init` (project) or `julius init -g` (global)"})
	}

	stashDir := execx.StashDir()
	if err := os.MkdirAll(stashDir, 0o755); err == nil {
		probe := filepath.Join(stashDir, ".doctor-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err == nil {
			_ = os.Remove(probe)
			checks = append(checks, Check{"raw-output stash writable", true, stashDir})
		} else {
			checks = append(checks, Check{"raw-output stash writable", false, err.Error()})
		}
	} else {
		checks = append(checks, Check{"raw-output stash writable", false, err.Error()})
	}

	if l, err := ledger.Open(ledger.DefaultPath()); err == nil {
		l.Close()
		checks = append(checks, Check{"savings ledger", true, ledger.DefaultPath()})
	} else {
		checks = append(checks, Check{"savings ledger", false, err.Error()})
	}

	return checks
}

// Render prints checks in a stable, greppable format and reports overall health.
func Render(checks []Check, w interface{ Write([]byte) (int, error) }) bool {
	ok := true
	for _, c := range checks {
		mark := ui.Good("PASS")
		if !c.OK {
			mark = ui.Bad("FAIL")
			ok = false
		}
		fmt.Fprintf(w, "%s  %-30s %s\n", mark, c.Name, ui.Dim(c.Detail))
	}
	return ok
}
