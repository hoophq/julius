// Package scan analyzes Claude Code session transcripts and reports what
// julius would have saved: commands that ran unwrapped are replayed through
// the filter engine against their recorded outputs, so the numbers are
// measured, not modeled.
package scan

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hoophq/julius/internal/filter"
	"github.com/hoophq/julius/internal/router"
	"github.com/hoophq/julius/internal/tokens"
)

// Report is the result of scanning a transcript directory.
type Report struct {
	Sessions     int
	BashCommands int
	Wrapped      int // already ran through julius
	Missed       []Missed
	Candidates   []Candidate
}

// Missed is a supported command that ran unwrapped; savings are computed
// by replaying the filter on the recorded output.
type Missed struct {
	Command      string
	Runs         int
	TokensBefore int
	TokensAfter  int
}

// Saved returns the tokens this command left on the table.
func (m Missed) Saved() int { return m.TokensBefore - m.TokensAfter }

// Coverage reports how much of the routable traffic actually went through
// julius: of the commands julius has a filter for, the fraction that ran
// wrapped. Candidates (no filter exists) are excluded — julius could not
// have helped them, so they don't belong in the denominator. This is the
// north-star for *realized* savings: a perfect filter on a command that
// bypasses the hook saves nothing.
//
// Returns the percentage plus the wrapped count and the routable total.
func (r Report) Coverage() (pct float64, wrapped, routable int) {
	wrapped = r.Wrapped
	routable = r.Wrapped
	for _, m := range r.Missed {
		routable += m.Runs
	}
	if routable == 0 {
		return 0, wrapped, routable
	}
	return float64(wrapped) / float64(routable) * 100, wrapped, routable
}

// Candidate is an unsupported command family ranked by output volume —
// the data-driven queue for new filters.
type Candidate struct {
	Family string // leading command word(s)
	Runs   int
	Tokens int
}

// TranscriptDir returns the Claude Code transcript directory for a project.
func TranscriptDir(projectDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	encoded := strings.ReplaceAll(projectDir, "/", "-")
	return filepath.Join(home, ".claude", "projects", encoded)
}

// transcript line shapes — only the fields scan needs.
type line struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type contentEntry struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input struct {
		Command string `json:"command"`
	} `json:"input"`
	ToolUseID string `json:"tool_use_id"`
}

type toolResult struct {
	Stdout string `json:"stdout"`
}

// Dir scans every *.jsonl transcript in dir modified within the window.
func Dir(dir string, since time.Time, reg *filter.Registry) (Report, error) {
	var rep Report
	entries, err := os.ReadDir(dir)
	if err != nil {
		return rep, err
	}

	missed := map[string]*Missed{}
	candidates := map[string]*Candidate{}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if info, err := e.Info(); err != nil || info.ModTime().Before(since) {
			continue
		}
		rep.Sessions++
		scanFile(filepath.Join(dir, e.Name()), reg, &rep, missed, candidates)
	}

	for _, m := range missed {
		rep.Missed = append(rep.Missed, *m)
	}
	sort.Slice(rep.Missed, func(i, j int) bool { return rep.Missed[i].Saved() > rep.Missed[j].Saved() })
	for _, c := range candidates {
		rep.Candidates = append(rep.Candidates, *c)
	}
	sort.Slice(rep.Candidates, func(i, j int) bool { return rep.Candidates[i].Tokens > rep.Candidates[j].Tokens })
	return rep, nil
}

func scanFile(path string, reg *filter.Registry, rep *Report, missed map[string]*Missed, candidates map[string]*Candidate) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	pending := map[string]string{} // tool_use id → command

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20) // transcript lines can be huge
	for sc.Scan() {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			continue
		}
		var entries []contentEntry
		if err := json.Unmarshal(l.Message.Content, &entries); err != nil {
			continue
		}
		switch l.Type {
		case "assistant":
			for _, c := range entries {
				if c.Type == "tool_use" && c.Name == "Bash" && c.Input.Command != "" {
					pending[c.ID] = c.Input.Command
				}
			}
		case "user":
			for _, c := range entries {
				if c.Type != "tool_result" {
					continue
				}
				cmd, ok := pending[c.ToolUseID]
				if !ok {
					continue
				}
				delete(pending, c.ToolUseID)
				var tr toolResult
				_ = json.Unmarshal(l.ToolUseResult, &tr)
				classify(cmd, tr.Stdout, reg, rep, missed, candidates)
			}
		}
	}
}

func classify(cmd, stdout string, reg *filter.Registry, rep *Report, missed map[string]*Missed, candidates map[string]*Candidate) {
	rep.BashCommands++

	for _, p := range router.SplitChain(cmd) {
		seg := p.Text
		if seg == "" {
			continue
		}
		if seg == "julius" || strings.HasPrefix(seg, "julius ") {
			rep.Wrapped++
			return
		}
	}

	// Empty recorded output: nothing was at stake, and replaying a filter
	// would only count its ack message as negative savings.
	if strings.TrimSpace(stdout) == "" {
		return
	}

	f := reg.Pick(cmd)
	if f == nil {
		// try per-segment for chains
		for _, p := range router.SplitChain(cmd) {
			if p.Text != "" && reg.Pick(p.Text) != nil {
				f = reg.Pick(p.Text)
				break
			}
		}
	}

	if f == nil {
		fam := family(cmd)
		c := candidates[fam]
		if c == nil {
			c = &Candidate{Family: fam}
			candidates[fam] = c
		}
		c.Runs++
		c.Tokens += tokens.Estimate(stdout)
		return
	}

	// Replay the filter on the recorded output: measured, not modeled.
	res := filter.Finalize(stdout, f.Apply(stdout, 0))
	key := f.Name()
	m := missed[key]
	if m == nil {
		m = &Missed{Command: key}
		missed[key] = m
	}
	m.Runs++
	m.TokensBefore += tokens.Estimate(stdout)
	m.TokensAfter += tokens.Estimate(res.Output)
}

// family reduces a command line to a rankable family name, e.g.
// "kubectl get pods -A" → "kubectl get".
func family(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "(empty)"
	}
	if len(fields) >= 2 && !strings.HasPrefix(fields[1], "-") {
		return fields[0] + " " + fields[1]
	}
	return fields[0]
}
