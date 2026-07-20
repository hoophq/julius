package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hoophq/julius/internal/execx"
	"github.com/hoophq/julius/internal/ledger"
	"github.com/hoophq/julius/internal/ui"
)

// Check is one doctor verification result.
type Check struct {
	Name   string
	OK     bool
	Detail string
	// Warn marks an advisory finding: rendered as WARN, never flips the
	// overall ok/exit state. Only meaningful when OK is true.
	Warn bool
}

// Doctor verifies the julius installation end to end.
func Doctor(cwd string) []Check {
	var checks []Check

	if path, err := exec.LookPath("julius"); err == nil {
		checks = append(checks, Check{Name: "binary on PATH", OK: true, Detail: path})
	} else {
		checks = append(checks, Check{Name: "binary on PATH", OK: false, Detail: "julius not found in PATH — hooks will fail silently"})
	}

	home, _ := os.UserHomeDir()
	preSrc, postSrc := hookSourceDetails(home, cwd)

	// The registration check walks the same settings files, with the same
	// labels, as the sources scan (user, project, project-local) so the two
	// checks can never contradict each other — e.g. hooks registered only
	// in settings.local.json.
	hookFound := false
	var hookWhere string
	for _, path := range settingsCandidates(home, cwd) {
		if Installed(path) {
			hookFound = true
			hookWhere = tildePath(home, path)
			break
		}
	}
	switch {
	case hookFound:
		checks = append(checks, Check{Name: "Claude Code hook registered", OK: true, Detail: hookWhere})
	default:
		if p, ok := firstPluginSource(postSrc, preSrc); ok {
			// A plugin-only install is healthy: the plugin invokes the
			// julius hooks, no settings.json entry needed.
			checks = append(checks, Check{Name: "Claude Code hook registered", OK: true,
				Detail: fmt.Sprintf("via plugin %s (%s)", p.pluginName, p.installPath)})
		} else {
			checks = append(checks, Check{Name: "Claude Code hook registered", OK: false, Detail: "run `julius init` (project) or `julius init -g` (global)"})
		}
	}

	if c, ok := hookSourcesCheck(preSrc, postSrc); ok {
		checks = append(checks, c)
	}

	stashDir := execx.StashDir()
	if err := os.MkdirAll(stashDir, 0o755); err == nil {
		probe := filepath.Join(stashDir, ".doctor-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err == nil {
			_ = os.Remove(probe)
			checks = append(checks, Check{Name: "raw-output stash writable", OK: true, Detail: stashDir})
		} else {
			checks = append(checks, Check{Name: "raw-output stash writable", OK: false, Detail: err.Error()})
		}
	} else {
		checks = append(checks, Check{Name: "raw-output stash writable", OK: false, Detail: err.Error()})
	}

	if l, err := ledger.Open(ledger.DefaultPath()); err == nil {
		l.Close()
		checks = append(checks, Check{Name: "savings ledger", OK: true, Detail: ledger.DefaultPath()})
	} else {
		checks = append(checks, Check{Name: "savings ledger", OK: false, Detail: err.Error()})
	}

	return checks
}

// firstPluginSource returns the first plugin-backed hook source across the
// given lists (post first — it is the event that fires on every matched
// tool result).
func firstPluginSource(lists ...[]hookSource) (hookSource, bool) {
	for _, list := range lists {
		for _, s := range list {
			if s.isPlugin() {
				return s, true
			}
		}
	}
	return hookSource{}, false
}

// hookSourcesCheck reports how many places invoke the julius hooks. One
// source per event is healthy. Two or more gets per-event advice: a
// settings+plugin mix means every matched tool call pays multiple hook
// round-trips (julius dedups the double event, so the output is correct —
// only latency is wasted), while settings-file-only duplicates with
// byte-identical commands are harmless — Claude Code dedups identical
// command strings across settings files and runs the hook once.
func hookSourcesCheck(pre, post []hookSource) (Check, bool) {
	if len(pre) == 0 && len(post) == 0 {
		return Check{}, false // nothing registered; the registration check already fails
	}

	type dup struct {
		event   string
		sources []hookSource
	}
	var dups []dup
	if len(pre) >= 2 {
		dups = append(dups, dup{"claude-pre", pre})
	}
	if len(post) >= 2 {
		dups = append(dups, dup{"claude-post", post})
	}

	if len(dups) == 0 {
		labels := map[string]bool{}
		var order []string
		for _, s := range append(append([]hookSource{}, pre...), post...) {
			if !labels[s.label] {
				labels[s.label] = true
				order = append(order, s.label)
			}
		}
		return Check{Name: "julius hook sources", OK: true,
			Detail: fmt.Sprintf("1 source per event (%s)", strings.Join(order, ", "))}, true
	}

	// Both events duplicated by the same sources collapse to one line:
	// claude-post is the representative (it fires on every matched tool).
	if len(dups) == 2 && sameLabels(dups[0].sources, dups[1].sources) {
		dups = dups[1:]
	}

	// Advice is computed per event: an event is only told to drop its
	// settings entries when the plugin duplicates that same event, and
	// identical settings-only duplicates are called harmless rather than
	// charged a phantom round-trip.
	var parts []string
	for _, d := range dups {
		var labels []string
		hasSettings, hasPlugin := false, false
		for _, s := range d.sources {
			labels = append(labels, s.label)
			if s.isPlugin() {
				hasPlugin = true
			} else {
				hasSettings = true
			}
		}
		var advice string
		switch {
		case hasSettings && hasPlugin:
			advice = "julius dedups the double event, but you pay two hook round-trips; remove the julius entries from settings.json (the plugin already runs them)"
		case identicalCommands(d.sources):
			advice = "the commands are identical, so Claude Code runs the hook once; the duplicate is harmless but redundant — remove the entry from one of these files"
		default:
			advice = "julius dedups the double event, but you pay two hook round-trips; remove the duplicate registration"
		}
		parts = append(parts, fmt.Sprintf("%d sources invoke julius hook %s: %s — %s",
			len(d.sources), d.event, strings.Join(labels, " + "), advice))
	}
	return Check{Name: "julius hook sources", OK: true, Warn: true, Detail: strings.Join(parts, "; ")}, true
}

// identicalCommands reports whether every duplicated source carries the
// same parsed hook command strings. Only then is a settings-file-only
// duplicate provably harmless — Claude Code dedups identical commands
// across settings files. Sources without parsed commands (plugins,
// unparseable settings) are never considered identical.
func identicalCommands(sources []hookSource) bool {
	var ref string
	for i, s := range sources {
		if len(s.commands) == 0 {
			return false
		}
		cmds := append([]string(nil), s.commands...)
		sort.Strings(cmds)
		joined := strings.Join(cmds, "\n")
		if i == 0 {
			ref = joined
		} else if joined != ref {
			return false
		}
	}
	return true
}

func sameLabels(a, b []hookSource) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[string]bool{}
	for _, s := range a {
		set[s.label] = true
	}
	for _, s := range b {
		if !set[s.label] {
			return false
		}
	}
	return true
}

// Render prints checks in a stable, greppable format and reports overall
// health. Warn checks print WARN but never fail the run.
func Render(checks []Check, w interface{ Write([]byte) (int, error) }) bool {
	ok := true
	for _, c := range checks {
		mark := ui.Good("PASS")
		switch {
		case !c.OK:
			mark = ui.Bad("FAIL")
			ok = false
		case c.Warn:
			mark = ui.Warn("WARN")
		}
		fmt.Fprintf(w, "%s  %-30s %s\n", mark, c.Name, ui.Dim(c.Detail))
	}
	return ok
}
