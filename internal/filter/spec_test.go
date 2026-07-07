package filter

import (
	"strings"
	"testing"
)

// TestBuiltinSpecs runs every inline test shipped inside builtin/*.toml
// and enforces that each builtin filter declares at least one test.
func TestBuiltinSpecs(t *testing.T) {
	specs := mustBuiltin()
	if len(specs) == 0 {
		t.Fatal("no builtin filters found")
	}
	for _, s := range specs {
		if len(s.Tests) == 0 {
			t.Errorf("builtin filter %q has no inline tests", s.Name())
			continue
		}
		for _, tc := range s.Tests {
			t.Run(s.Name()+"/"+tc.Name, func(t *testing.T) {
				got := Finalize(tc.Input, s.Apply(tc.Input, tc.ExitCode))
				want := strings.TrimRight(tc.Want, "\n")
				if strings.TrimRight(got.Output, "\n") != want {
					t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", got.Output, want)
				}
			})
		}
	}
}

func TestFinalizeInvariants(t *testing.T) {
	// Filter output larger than raw input: raw wins.
	r := Finalize("{}", Result{Output: "{\n  \"pretty\": true\n}", Applied: true})
	if r.Output != "{}" {
		t.Errorf("larger output must fall back to raw, got %q", r.Output)
	}
	// Filter emptied non-empty input: raw wins.
	r = Finalize("data", Result{Output: "", Applied: true})
	if r.Output != "data" {
		t.Errorf("emptied output must fall back to raw, got %q", r.Output)
	}
	// Empty raw input: a terse ack from if_empty is allowed.
	r = Finalize("", Result{Output: "ok", Applied: true})
	if r.Output != "ok" {
		t.Errorf("ack on empty raw must survive, got %q", r.Output)
	}
	// Passthrough keeps raw.
	r = Finalize("data", Result{Output: "ignored", Applied: false})
	if r.Output != "data" {
		t.Errorf("passthrough must keep raw, got %q", r.Output)
	}
	// Smaller applied output survives.
	r = Finalize("a very long raw output here", Result{Output: "ok", Applied: true})
	if r.Output != "ok" {
		t.Errorf("smaller output must survive, got %q", r.Output)
	}
}

func TestSpecPipeline(t *testing.T) {
	src := []byte(`
[filters.demo]
description = "demo"
command = '^demo\b'
strip_ansi = true
replace = [{ pattern = 'tok-[a-f0-9]+', with = 'tok-***' }]
respond = [{ pattern = 'all clean', message = 'demo: ok', unless = 'ERROR' }]
drop_lines = ['^noise']
max_line_length = 20
head = 3
if_empty = "demo: nothing"
`)
	specs, err := ParseFile(src)
	if err != nil {
		t.Fatal(err)
	}
	s := specs[0]

	if !s.MatchCommand("demo run") || s.MatchCommand("other") {
		t.Fatal("MatchCommand misbehaves")
	}

	// respond short-circuits
	if got := s.Apply("stuff\nall clean\n", 0); got.Output != "demo: ok" {
		t.Errorf("respond: got %q", got.Output)
	}
	// respond suppressed by unless
	if got := s.Apply("all clean\nERROR: no\n", 0); got.Output == "demo: ok" {
		t.Error("respond fired despite unless")
	}
	// replace masks secrets
	if got := s.Apply("token tok-deadbeef here", 0); !strings.Contains(got.Output, "tok-***") {
		t.Errorf("replace: got %q", got.Output)
	}
	// drop + head + truncation
	long := "noise 1\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nb\nc\nd\ne\n"
	got := s.Apply(long, 0)
	lines := strings.Split(got.Output, "\n")
	if len(lines) != 4 || !strings.HasSuffix(lines[0], "…") || lines[3] != "(+2 more lines)" {
		t.Errorf("pipeline: got %q", got.Output)
	}
	// if_empty
	if got := s.Apply("noise only\n", 0); got.Output != "demo: nothing" {
		t.Errorf("if_empty: got %q", got.Output)
	}
}
