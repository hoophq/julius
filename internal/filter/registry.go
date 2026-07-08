package filter

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed builtin/*.toml
var builtinFS embed.FS

// Registry resolves a command line to the filter that handles it.
// Lookup order, first match wins: code filters, project filters
// (<project>/.julius/filters.toml), user filters
// (<user config dir>/julius/filters.toml), builtin filters.
type Registry struct {
	code    []Filter
	project []*Spec
	user    []*Spec
	builtin []*Spec
}

// Load builds a Registry rooted at projectDir (usually the cwd).
// Broken project/user filter files are skipped with a warning on stderr —
// a bad custom filter must never break command execution.
func Load(projectDir string) *Registry {
	r := &Registry{builtin: mustBuiltin()}
	r.project = loadTier(filepath.Join(projectDir, ".julius", "filters.toml"))
	if dir, err := os.UserConfigDir(); err == nil {
		r.user = loadTier(filepath.Join(dir, "julius", "filters.toml"))
	}
	return r
}

// Register adds a code filter. Code filters take precedence over specs.
func (r *Registry) Register(f Filter) { r.code = append(r.code, f) }

// Pick returns the filter handling cmd, or nil for passthrough.
func (r *Registry) Pick(cmd string) Filter {
	for _, f := range r.code {
		if f.MatchCommand(cmd) {
			return f
		}
	}
	for _, tier := range [][]*Spec{r.project, r.user, r.builtin} {
		for _, s := range tier {
			if s.MatchCommand(cmd) {
				return s
			}
		}
	}
	return nil
}

// Builtin exposes the embedded specs (used by tests and docs generation).
func (r *Registry) Builtin() []*Spec { return r.builtin }

// Sniff returns the spec whose detect_output patterns match the raw text,
// or nil. Used when output arrives without a trustworthy command line
// (native tool results, unrouted commands).
func (r *Registry) Sniff(text string) *Spec {
	for _, tier := range [][]*Spec{r.project, r.user, r.builtin} {
		for _, s := range tier {
			if s.MatchOutput(text) {
				return s
			}
		}
	}
	return nil
}

func loadTier(path string) []*Spec {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	specs, err := ParseFile(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[julius] warning: skipping %s: %v\n", path, err)
		return nil
	}
	return specs
}

func mustBuiltin() []*Spec {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		panic(fmt.Sprintf("builtin filters: %v", err))
	}
	var specs []*Spec
	for _, e := range entries {
		data, err := builtinFS.ReadFile("builtin/" + e.Name())
		if err != nil {
			panic(fmt.Sprintf("builtin %s: %v", e.Name(), err))
		}
		parsed, err := ParseFile(data)
		if err != nil {
			// Builtin filters are validated by go test; a parse failure
			// here is a programmer error, not a user-recoverable state.
			panic(fmt.Sprintf("builtin %s: %v", e.Name(), err))
		}
		specs = append(specs, parsed...)
	}
	return specs
}
