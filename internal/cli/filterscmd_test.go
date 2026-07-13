package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFilterFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "filters.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTestFilterFilesPassAndFail(t *testing.T) {
	path := writeFilterFile(t, `
[filters.demo]
command = '^demo\b'
drop_lines = ['^noise']
if_empty = "demo: ok"

[[filters.demo.tests]]
name = "noise collapses"
input = "noise a\nnoise b\n"
want = "demo: ok"

[[filters.demo.tests]]
name = "wrong expectation"
input = "keep me\n"
want = "something else"

[filters.untested]
command = '^untested\b'
drop_lines = ['^x']
`)
	reports := testFilterFiles([]string{path})
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	rep := reports[0]
	if rep.ReadErr != nil || rep.ParseErr != nil {
		t.Fatalf("unexpected errors: %+v", rep)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(rep.Results))
	}
	if !rep.Results[0].Pass {
		t.Errorf("passing case reported as failure: %+v", rep.Results[0])
	}
	if rep.Results[1].Pass {
		t.Errorf("failing case reported as pass: %+v", rep.Results[1])
	}
	if rep.Results[1].Got != "keep me" || rep.Results[1].Want != "something else" {
		t.Errorf("got/want not captured: %+v", rep.Results[1])
	}
	if len(rep.NoTests) != 1 || rep.NoTests[0] != "untested" {
		t.Errorf("no-tests filters = %v", rep.NoTests)
	}
	if rep.Failures() != 1 {
		t.Errorf("failures = %d, want 1", rep.Failures())
	}
}

// A file that fails to parse or compile is a failure — the lint role of
// the command: at runtime the same problem is only a skip-warning.
func TestTestFilterFilesParseError(t *testing.T) {
	bad := writeFilterFile(t, "not toml {{")
	badRegex := writeFilterFile(t, `
[filters.broken]
command = '['
`)
	missing := filepath.Join(t.TempDir(), "nope.toml")

	reports := testFilterFiles([]string{bad, badRegex, missing})
	if len(reports) != 3 {
		t.Fatalf("reports = %d, want 3", len(reports))
	}
	if reports[0].ParseErr == nil {
		t.Error("invalid TOML must be a parse failure")
	}
	if reports[1].ParseErr == nil {
		t.Error("invalid regex must be a compile failure")
	}
	if reports[2].ReadErr == nil {
		t.Error("missing file must be a read failure")
	}
	for i, rep := range reports {
		if rep.Failures() != 1 {
			t.Errorf("report %d failures = %d, want 1", i, rep.Failures())
		}
	}
}

// Unnamed cases get positional labels so failures stay addressable.
func TestTestFilterFilesUnnamedCase(t *testing.T) {
	path := writeFilterFile(t, `
[filters.demo]
command = '^demo\b'
if_empty = "ok"

[[filters.demo.tests]]
input = ""
want = "ok"
`)
	reports := testFilterFiles([]string{path})
	if len(reports[0].Results) != 1 || reports[0].Results[0].Test != "#1" {
		t.Fatalf("unnamed case label: %+v", reports[0].Results)
	}
	if !reports[0].Results[0].Pass {
		t.Errorf("if_empty ack case should pass: %+v", reports[0].Results[0])
	}
}
