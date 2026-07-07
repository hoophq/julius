package claude

import "testing"

func TestEvaluatePrecedence(t *testing.T) {
	r := Rules{
		Allow: []string{"Bash(git status)", "Bash(git log:*)"},
		Ask:   []string{"Bash(git push:*)"},
		Deny:  []string{"Bash(git push --force:*)", "Bash(rm *)"},
	}
	cases := []struct {
		cmd  string
		want Verdict
	}{
		{"git status", VerdictAllow},
		{"git log --oneline", VerdictAllow},
		{"git push", VerdictAsk},
		{"git push origin main", VerdictAsk},
		{"git push --force origin main", VerdictDeny}, // deny beats ask
		{"rm -rf /tmp/x", VerdictDeny},
		{"go test ./...", VerdictNone},
	}
	for _, c := range cases {
		if got := r.Evaluate(c.cmd); got != c.want {
			t.Errorf("Evaluate(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

func TestEvaluateChain(t *testing.T) {
	r := Rules{
		Allow: []string{"Bash(git status)", "Bash(git add:*)"},
		Deny:  []string{"Bash(git push --force:*)"},
	}
	// worst verdict wins
	if got := r.EvaluateChain([]string{"git status", "git push --force"}); got != VerdictDeny {
		t.Errorf("chain with deny = %v, want VerdictDeny", got)
	}
	// allow requires every segment allowed
	if got := r.EvaluateChain([]string{"git status", "go test"}); got != VerdictNone {
		t.Errorf("chain allow+none = %v, want VerdictNone", got)
	}
	if got := r.EvaluateChain([]string{"git status", "git add ."}); got != VerdictAllow {
		t.Errorf("chain all-allow = %v, want VerdictAllow", got)
	}
}

func TestMatchSpecForms(t *testing.T) {
	if !matchAnyRule([]string{"Bash"}, "anything at all") {
		t.Error("bare Bash must match everything")
	}
	if !matchAnyRule([]string{"Bash(*)"}, "anything") {
		t.Error("Bash(*) must match everything")
	}
	if matchAnyRule([]string{"Bash(git status)"}, "git status --short") {
		t.Error("exact rule must not prefix-match")
	}
	if !matchAnyRule([]string{"Bash(npm run:*)"}, "npm run build") {
		t.Error(":* rule must prefix-match")
	}
	if !matchAnyRule([]string{"Bash(git *)"}, "git commit -m x") {
		t.Error("space-star rule must prefix-match")
	}
	if matchAnyRule([]string{"Read(*)"}, "git status") {
		t.Error("non-Bash scopes must be ignored")
	}
}
