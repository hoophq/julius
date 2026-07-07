package filter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hoophq/julius/internal/tokens"
)

// corpusCase is a representative raw output for a supported command,
// sized like real agent-session traffic (not the tiny inline-test samples).
type corpusCase struct {
	cmd      string
	raw      string
	minSaved float64 // per-command floor, %
}

func buildCorpus() []corpusCase {
	var goTest strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&goTest, "=== RUN   TestCase%02d\n--- PASS: TestCase%02d (0.0%ds)\n", i, i, i%9)
	}
	goTest.WriteString("PASS\nok  \tgithub.com/hoophq/julius/internal/filter\t1.204s\n")

	var pytest strings.Builder
	pytest.WriteString("platform darwin -- Python 3.12.0, pytest-8.0.0\nrootdir: /app\nplugins: cov-4.1.0, asyncio-0.23\ncollected 48 items\n\n")
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&pytest, "tests/test_module_%02d.py ....                                        [%3d%%]\n", i, (i+1)*100/12)
	}
	pytest.WriteString("\n============================== 48 passed in 2.31s ==============================\n")

	var gitStatus strings.Builder
	gitStatus.WriteString("On branch main\nYour branch is up to date with 'origin/main'.\n\nChanges not staged for commit:\n  (use \"git add <file>...\" to update what will be committed)\n  (use \"git restore <file>...\" to discard changes in working directory)\n")
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&gitStatus, "\tmodified:   internal/pkg%02d/file%02d.go\n", i, i)
	}
	gitStatus.WriteString("\nUntracked files:\n  (use \"git add <file>...\" to include in what will be committed)\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&gitStatus, "\tinternal/new%02d/\n", i)
	}
	gitStatus.WriteString("\nno changes added to commit (use \"git add\" and/or \"git commit -a\")\n")

	var npm strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&npm, "npm timing reifyNode:node_modules/pkg%02d Completed in %dms\n", i, i*7)
	}
	npm.WriteString("\nadded 214 packages, and audited 215 packages in 4s\n\n42 packages are looking for funding\n  run `npm fund` for details\n\nfound 0 vulnerabilities\n")

	var push strings.Builder
	push.WriteString("Enumerating objects: 124, done.\nCounting objects: 100% (124/124), done.\nDelta compression using up to 10 threads\nCompressing objects: 100% (84/84), done.\nWriting objects: 100% (95/95), 48.72 KiB | 9.74 MiB/s, done.\nTotal 95 (delta 36), reused 0 (delta 0), pack-reused 0\nremote: Resolving deltas: 100% (36/36), completed with 12 local objects.\nTo github.com:hoophq/julius.git\n   0dabaa5..09e5a22  main -> main\n")

	var jest strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&jest, "PASS src/feature%d/api.test.ts\n", i)
		for j := 0; j < 6; j++ {
			fmt.Fprintf(&jest, "  ✓ scenario %d-%d works as expected (%d ms)\n", i, j, j*3+1)
		}
	}
	jest.WriteString("\nTest Suites: 8 passed, 8 total\nTests:       48 passed, 48 total\nSnapshots:   0 total\nTime:        3.87 s\nRan all test suites.\n")

	return []corpusCase{
		{"go test ./...", goTest.String(), 85},
		{"pytest", pytest.String(), 75},
		{"git status", gitStatus.String(), 35},
		{"npm install", npm.String(), 85},
		{"git push origin main", push.String(), 70},
		{"npx jest", jest.String(), 85},
	}
}

// TestCorpusSavings is the M1 acceptance gate: the filter wave must save
// at least 60% of tokens on average across representative outputs.
func TestCorpusSavings(t *testing.T) {
	reg := Load(t.TempDir()) // no project/user tiers: builtins only
	var total float64
	for _, c := range buildCorpus() {
		f := reg.Pick(c.cmd)
		if f == nil {
			t.Fatalf("no filter for corpus command %q", c.cmd)
		}
		res := Finalize(c.raw, f.Apply(c.raw, 0))
		saved := tokens.SavedPercent(c.raw, res.Output)
		t.Logf("%-22s %5.1f%% saved (%d → %d tokens) via %s",
			c.cmd, saved, tokens.Estimate(c.raw), tokens.Estimate(res.Output), f.Name())
		if saved < c.minSaved {
			t.Errorf("%s: saved %.1f%%, want ≥ %.0f%%\nfiltered output:\n%s", c.cmd, saved, c.minSaved, res.Output)
		}
		total += saved
	}
	avg := total / float64(len(buildCorpus()))
	t.Logf("average savings: %.1f%%", avg)
	if avg < 60 {
		t.Errorf("average corpus savings %.1f%%, acceptance floor is 60%%", avg)
	}
}
