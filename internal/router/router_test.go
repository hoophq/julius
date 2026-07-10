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
		// env-prefixed: assignments stay ahead of julius, reach the child
		{"CGO_ENABLED=0 go test ./...", "CGO_ENABLED=0 julius go test ./...", true},
		{"NODE_ENV=test FOO=bar go test", "NODE_ENV=test FOO=bar julius go test", true},
		{`FOO="a b" go test`, `FOO="a b" julius go test`, true},
		// env-prefixed but not routable → untouched
		{"FOO=bar ls -la", "FOO=bar ls -la", false},
		// already-wrapped after an env prefix → idempotent
		{"FOO=bar julius go test", "FOO=bar julius go test", false},
		// env prefix on one segment of a chain
		{"CGO_ENABLED=0 go test ./... && ls", "CGO_ENABLED=0 julius go test ./... && ls", true},
		// sudo: julius wraps the whole sudo invocation, running as the user
		{"sudo git status", "julius sudo git status", true},
		{"sudo -E git status", "julius sudo -E git status", true},
		{"sudo -u deploy git status", "julius sudo -u deploy git status", true},
		{"FOO=bar sudo git status", "FOO=bar julius sudo git status", true},
		// shell-invoking sudo forms are left alone
		{"sudo -i git status", "sudo -i git status", false},
		// path-invoked executables match by basename, run by original path
		{"/usr/bin/git status", "julius /usr/bin/git status", true},
		{"./scripts/git status", "julius ./scripts/git status", true},
		// already wrapped in path or sudo spelling → idempotent
		{"./julius git status", "./julius git status", false},
		{"/usr/local/bin/julius git status", "/usr/local/bin/julius git status", false},
		{"sudo julius git status", "sudo julius git status", false},
	}
	for _, c := range cases {
		got, changed := Route(c.in, gitOrGoTest)
		if got != c.want || changed != c.wantChanged {
			t.Errorf("Route(%q) = (%q, %v), want (%q, %v)", c.in, got, changed, c.want, c.wantChanged)
		}
	}
}

func TestMatchTarget(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git status", "git status"},
		{"CGO_ENABLED=0 go test ./...", "go test ./..."},
		{"sudo docker ps", "docker ps"},
		{"sudo -E -n kubectl get pods", "kubectl get pods"},
		{"sudo -u root -H docker ps", "docker ps"},
		{"sudo FOO=bar go test", "go test"},
		{"sudo -E /usr/bin/git status", "git status"},
		{"/opt/homebrew/bin/gh pr list", "gh pr list"},
		{"./julius scan", "julius scan"},
		{"node_modules/.bin/jest --ci", "jest --ci"},
		// unreducible forms come back unchanged (minus env prefix)
		{"sudo -i git status", "sudo -i git status"},
		{"sudo --badflag git status", "sudo --badflag git status"},
		{`"/my dir/git" status`, `"/my dir/git" status`},
		// degenerate inputs must not panic
		{"sudo", ""},
		{"", ""},
		{"/", "/"},
	}
	for _, c := range cases {
		if got := MatchTarget(c.in); got != c.want {
			t.Errorf("MatchTarget(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsWrapped(t *testing.T) {
	wrapped := []string{"julius git status", "julius", "./julius scan", "/usr/local/bin/julius go test", "sudo julius docker ps", "FOO=bar julius go test"}
	for _, s := range wrapped {
		if !IsWrapped(s) {
			t.Errorf("IsWrapped(%q) = false, want true", s)
		}
	}
	unwrapped := []string{"git status", "juliusish tool", "echo julius git status | cat"}
	for _, s := range unwrapped {
		if IsWrapped(s) {
			t.Errorf("IsWrapped(%q) = true, want false", s)
		}
	}
}
