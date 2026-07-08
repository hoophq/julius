package filter

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// File is the on-disk shape of a filters.toml document.
type File struct {
	Filters map[string]*Spec `toml:"filters"`
}

// Spec is a declarative filter. Stages run in this order:
//
//	strip_ansi → replace → respond → keep_lines → drop_lines →
//	max_line_length → head/tail → if_empty
type Spec struct {
	Description string        `toml:"description"`
	Command     string        `toml:"command"`       // regex matched against the command line
	DetectOutput []string     `toml:"detect_output"` // regexes matched against raw OUTPUT (content sniffing)
	StripANSI   bool          `toml:"strip_ansi"`
	MergeStderr bool          `toml:"merge_stderr"`
	Replace     []Replacement `toml:"replace"`
	Respond     []Responder   `toml:"respond"`
	KeepLines   []string      `toml:"keep_lines"`
	DropLines   []string      `toml:"drop_lines"`
	MaxLineLen  int           `toml:"max_line_length"`
	Head        int           `toml:"head"`
	Tail        int           `toml:"tail"`
	IfEmpty     string        `toml:"if_empty"`
	Tests       []SpecTest    `toml:"tests"`

	name    string
	cmdRe   *regexp.Regexp
	detRe   []*regexp.Regexp
	replRe  []*regexp.Regexp
	respRe  []*regexp.Regexp
	unlRe   []*regexp.Regexp
	keepRe  []*regexp.Regexp
	dropRe  []*regexp.Regexp
}

// Replacement is a regex substitution applied line by line.
type Replacement struct {
	Pattern string `toml:"pattern"`
	With    string `toml:"with"`
}

// Responder short-circuits the pipeline: when Pattern matches anywhere in
// the output (and Unless does not), the entire output becomes Message.
type Responder struct {
	Pattern string `toml:"pattern"`
	Message string `toml:"message"`
	Unless  string `toml:"unless"`
}

// SpecTest is an inline test case shipped with a filter.
type SpecTest struct {
	Name     string `toml:"name"`
	Input    string `toml:"input"`
	Want     string `toml:"want"`
	ExitCode int    `toml:"exit_code"`
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// ParseFile parses a filters.toml document and compiles every spec.
// Specs are returned in name order — lookup must be deterministic even
// when matchers overlap.
func ParseFile(data []byte) ([]*Spec, error) {
	var f File
	if err := toml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(f.Filters))
	for name := range f.Filters {
		names = append(names, name)
	}
	sort.Strings(names)
	specs := make([]*Spec, 0, len(names))
	for _, name := range names {
		s := f.Filters[name]
		if err := s.compile(name); err != nil {
			return nil, fmt.Errorf("filter %q: %w", name, err)
		}
		specs = append(specs, s)
	}
	return specs, nil
}

func (s *Spec) compile(name string) error {
	s.name = name
	if s.Command == "" {
		return fmt.Errorf("missing required field: command")
	}
	var err error
	if s.cmdRe, err = regexp.Compile(s.Command); err != nil {
		return fmt.Errorf("command: %w", err)
	}
	for _, p := range s.DetectOutput {
		re, err := regexp.Compile("(?m)" + p)
		if err != nil {
			return fmt.Errorf("detect_output %q: %w", p, err)
		}
		s.detRe = append(s.detRe, re)
	}
	for _, r := range s.Replace {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return fmt.Errorf("replace %q: %w", r.Pattern, err)
		}
		s.replRe = append(s.replRe, re)
	}
	for _, r := range s.Respond {
		re, err := regexp.Compile("(?m)" + r.Pattern)
		if err != nil {
			return fmt.Errorf("respond %q: %w", r.Pattern, err)
		}
		s.respRe = append(s.respRe, re)
		var unl *regexp.Regexp
		if r.Unless != "" {
			if unl, err = regexp.Compile("(?m)" + r.Unless); err != nil {
				return fmt.Errorf("respond unless %q: %w", r.Unless, err)
			}
		}
		s.unlRe = append(s.unlRe, unl)
	}
	for _, p := range s.KeepLines {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("keep_lines %q: %w", p, err)
		}
		s.keepRe = append(s.keepRe, re)
	}
	for _, p := range s.DropLines {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("drop_lines %q: %w", p, err)
		}
		s.dropRe = append(s.dropRe, re)
	}
	return nil
}

// Name implements Filter.
func (s *Spec) Name() string { return s.name }

// MatchCommand implements Filter.
func (s *Spec) MatchCommand(cmd string) bool { return s.cmdRe.MatchString(cmd) }

// MatchOutput reports whether raw output looks like the format this spec
// filters, independent of which command produced it. Specs without
// detect_output patterns never match by content.
func (s *Spec) MatchOutput(text string) bool {
	for _, re := range s.detRe {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// Apply implements Filter. Callers must pass the result through Finalize.
func (s *Spec) Apply(raw string, exitCode int) Result {
	// Normalize CRLF so line anchors behave identically on Windows output
	// (and the \r bytes were only ever costing tokens).
	out := strings.ReplaceAll(raw, "\r\n", "\n")
	if s.StripANSI {
		out = ansiRe.ReplaceAllString(out, "")
	}

	if len(s.replRe) > 0 {
		lines := strings.Split(out, "\n")
		for i, line := range lines {
			for j, re := range s.replRe {
				line = re.ReplaceAllString(line, s.Replace[j].With)
			}
			lines[i] = line
		}
		out = strings.Join(lines, "\n")
	}

	for i, re := range s.respRe {
		if !re.MatchString(out) {
			continue
		}
		if s.unlRe[i] != nil && s.unlRe[i].MatchString(out) {
			continue
		}
		return Result{Output: s.Respond[i].Message, Applied: true}
	}

	if len(s.keepRe) > 0 || len(s.dropRe) > 0 {
		kept := make([]string, 0, 64)
		for _, line := range strings.Split(out, "\n") {
			if len(s.keepRe) > 0 && !matchAny(s.keepRe, line) {
				continue
			}
			if matchAny(s.dropRe, line) {
				continue
			}
			kept = append(kept, line)
		}
		out = strings.Join(kept, "\n")
	}

	if s.MaxLineLen > 0 {
		lines := strings.Split(out, "\n")
		for i, line := range lines {
			if r := []rune(line); len(r) > s.MaxLineLen {
				lines[i] = string(r[:s.MaxLineLen]) + "…"
			}
		}
		out = strings.Join(lines, "\n")
	}

	if s.Head > 0 || s.Tail > 0 {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if s.Head > 0 && len(lines) > s.Head {
			omitted := len(lines) - s.Head
			lines = append(lines[:s.Head], fmt.Sprintf("(+%d more lines)", omitted))
		}
		if s.Tail > 0 && len(lines) > s.Tail {
			omitted := len(lines) - s.Tail
			lines = append([]string{fmt.Sprintf("(%d earlier lines omitted)", omitted)}, lines[len(lines)-s.Tail:]...)
		}
		out = strings.Join(lines, "\n")
	}

	if strings.TrimSpace(out) == "" && s.IfEmpty != "" {
		out = s.IfEmpty
	}

	return Result{Output: strings.TrimRight(out, "\n"), Applied: true}
}

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
