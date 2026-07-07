package router

import (
	"strings"
	"testing"
)

func gitOrGoTest(cmd string) bool {
	return strings.HasPrefix(cmd, "git ") || strings.HasPrefix(cmd, "go test")
}

func TestSplitChain(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"git status", []string{"git status"}},
		{"git add . && git commit -m 'x'", []string{"git add .", "git commit -m 'x'"}},
		{"a | b || c ; d", []string{"a", "b", "c", "d"}},
		// separators inside quotes are not split points
		{`echo "a && b"`, []string{`echo "a && b"`}},
		{`echo 'a; b'`, []string{`echo 'a; b'`}},
		// subshells and backticks are opaque
		{"echo $(date; date)", []string{"echo $(date; date)"}},
		{"echo `date; date`", []string{"echo `date; date`"}},
		// escaped separators
		{`echo a\;b`, []string{`echo a\;b`}},
	}
	for _, c := range cases {
		parts := SplitChain(c.in)
		var got []string
		for _, p := range parts {
			got = append(got, p.Text)
		}
		if len(got) != len(c.want) {
			t.Errorf("SplitChain(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("SplitChain(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

func TestRoute(t *testing.T) {
	cases := []struct {
		in          string
		want        string
		wantChanged bool
	}{
		{"git status", "julius git status", true},
		{"ls -la", "ls -la", false},
		{"git add . && git commit -m 'wip'", "julius git add . && julius git commit -m 'wip'", true},
		{"git status | head -3", "julius git status | head -3", true},
		// idempotence: already wrapped stays untouched
		{"julius git status", "julius git status", false},
		{"julius git add . && git push", "julius git add . && julius git push", true},
		// mixed routable and not
		{"cd /tmp && go test ./...", "cd /tmp && julius go test ./...", true},
	}
	for _, c := range cases {
		got, changed := Route(c.in, gitOrGoTest)
		if got != c.want || changed != c.wantChanged {
			t.Errorf("Route(%q) = (%q, %v), want (%q, %v)", c.in, got, changed, c.want, c.wantChanged)
		}
	}
}
