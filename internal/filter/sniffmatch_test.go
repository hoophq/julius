package filter

import (
	"strings"
	"testing"
)

// (?m)$ never matches before \r, so sniffing must normalize CRLF exactly
// like Apply does — otherwise every $-anchored detect_output pattern
// silently fails on Windows-style output.
func TestMatchOutputNormalizesCRLF(t *testing.T) {
	reg := Load(t.TempDir()) // builtins only

	cases := map[string]string{
		"python": "Traceback (most recent call last):\n  File \"x.py\", line 1, in <module>\n    boom()\nValueError: boom\n",
		"pytest": "collected 12 items\ntests/test_api.py ............\n============ 12 passed in 0.41s ============\n",
	}
	for want, lf := range cases {
		crlf := strings.ReplaceAll(lf, "\n", "\r\n")
		if s := reg.Sniff(lf); s == nil || s.Name() != want {
			t.Errorf("LF %s output must sniff to %q, got %v", want, want, s)
		}
		if s := reg.Sniff(crlf); s == nil || s.Name() != want {
			t.Errorf("CRLF %s output must sniff to %q, got %v — $-anchored patterns need CRLF normalization", want, want, s)
		}
	}
}

// The python matcher must take whole interpreter tokens only: \b alone
// also matched python3-config, dragging merge_stderr semantics onto an
// unrelated command.
func TestPythonCommandMatchBoundaries(t *testing.T) {
	reg := Load(t.TempDir())

	for _, cmd := range []string{"python3 script.py", "python -c 'print(1)'", "python3.12 tool.py", "python3 <<'PY'\nprint(1)\nPY"} {
		if f := reg.Pick(cmd); f == nil || f.Name() != "python" {
			t.Errorf("%q must route to the python filter, got %v", cmd, f)
		}
	}
	for _, cmd := range []string{"python3-config --includes", "python3.12-config --ldflags", "pythonw app.py"} {
		if f := reg.Pick(cmd); f != nil && f.Name() == "python" {
			t.Errorf("%q must not route to the python filter", cmd)
		}
	}
	// -m forms keep their own filters via registry order
	if f := reg.Pick("python -m pytest tests/"); f == nil || f.Name() != "pytest" {
		t.Errorf("python -m pytest must route to pytest, got %v", f)
	}
	if f := reg.Pick("python3 -m pip install requests"); f == nil || f.Name() != "pip-install" {
		t.Errorf("python3 -m pip install must keep its own filter, got %v", f)
	}
}
