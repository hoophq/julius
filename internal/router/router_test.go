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
		// >| is a clobber redirect, not a pipe
		{"git status >| out.txt", []string{"git status >| out.txt"}},
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
		// pipe-feeding segments are never wrapped: a filter would truncate
		// the next command's stdin and inject marker lines as data
		{"git status | head -3", "git status | head -3", false},
		{"foo | git status && bar | go test ./...", "foo | julius git status && bar | julius go test ./...", true},
		{"git status | head && git log", "git status | head && julius git log", true},
		{"git status ; git log | cat", "julius git status ; git log | cat", true},
		// || chains segments, it does not pipe them
		{"git status || git log", "julius git status || julius git log", true},
		// a quoted pipe is text, not a pipe
		{`echo "a | b" && git status`, `echo "a | b" && julius git status`, true},
		// stdout-redirected segments are never wrapped: the file must
		// receive the raw output, not julius-filtered content
		{"git status > /tmp/x.txt", "git status > /tmp/x.txt", false},
		{"git status >> f", "git status >> f", false},
		{"git status 1> f", "git status 1> f", false},
		{"git log &> f", "git log &> f", false},
		{"git status >| f", "git status >| f", false},
		// stderr-only redirections leave stdout on the caller's terminal
		{"git status 2>/dev/null", "julius git status 2>/dev/null", true},
		{"git status 2>&1", "julius git status 2>&1", true},
		// a quoted '>' is data, not a redirect
		{`echo "a > b" && git status`, `echo "a > b" && julius git status`, true},
		{`git commit -m "a > b"`, `julius git commit -m "a > b"`, true},
		// a redirect disqualifies only its own segment
		{"git status > f && git log", "git status > f && julius git log", true},
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

func TestPartTerminal(t *testing.T) {
	cases := []struct {
		sep  string
		want bool
	}{
		{"", true},
		{"&&", true},
		{"||", true},
		{";", true},
		{"|", false},
	}
	for _, c := range cases {
		if got := (Part{Sep: c.sep}).Terminal(); got != c.want {
			t.Errorf("Part{Sep: %q}.Terminal() = %v, want %v", c.sep, got, c.want)
		}
	}
}

func TestPartStdoutRedirected(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"git status", false},
		{"git status > /tmp/x.txt", true},
		{"git status >/tmp/x.txt", true},
		{"git status >> log", true},
		{"git status 1> f", true},
		{"git status 1>> f", true},
		{"git log &> f", true},
		{"git log &>> f", true},
		{"git status >| f", true},
		{"git status >& f", true},
		// stderr-only forms and descriptor duplications keep stdout on the caller
		{"git status 2> /dev/null", false},
		{"git status 2>/dev/null", false},
		{"git status 2>> err.log", false},
		{"git status 2>&1", false},
		{"git status >&2", false},
		{"git status 1>&2", false},
		{"go test ./... 3> trace", false},
		// quoted, escaped, and nested '>' are data, not redirects
		{`echo "a > b"`, false},
		{`echo 'a > b'`, false},
		{`echo a\>b`, false},
		{"echo $(cat > f)", false},
		// a stderr redirect does not mask a real stdout redirect
		{"git status 2>/dev/null > out", true},
	}
	for _, c := range cases {
		if got := (Part{Text: c.text}).StdoutRedirected(); got != c.want {
			t.Errorf("Part{Text: %q}.StdoutRedirected() = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestRouteNeverTouchesPipeFeeders pins the invariant behind ATR-149: no
// segment whose stdout feeds a pipe is ever rewritten, even under a matcher
// that routes everything, so no filter marker can be injected upstream of a
// consumer.
func TestRouteNeverTouchesPipeFeeders(t *testing.T) {
	everything := func(string) bool { return true }
	pipelines := []string{
		"git status | head -3",
		"foo | bar | baz",
		"a | b && c | d",
		"git log --oneline | wc -l ; git status | cat",
		`echo "x | y" | grep x`,
		"CGO_ENABLED=0 go test -v ./... | tail -20 || make build | tee out.log",
	}
	for _, in := range pipelines {
		out, _ := Route(in, everything)
		inParts := SplitChain(in)
		outParts := SplitChain(out)
		if len(outParts) != len(inParts) {
			t.Errorf("Route(%q) changed segment count: %q", in, out)
			continue
		}
		for i, p := range inParts {
			if outParts[i].Sep != p.Sep {
				t.Errorf("Route(%q) changed separator %d: %q → %q", in, i, p.Sep, outParts[i].Sep)
			}
			if !p.Terminal() && outParts[i].Text != p.Text {
				t.Errorf("Route(%q) rewrote pipe-feeding segment %q → %q", in, p.Text, outParts[i].Text)
			}
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
