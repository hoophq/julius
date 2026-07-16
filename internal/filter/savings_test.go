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

	var cargo strings.Builder
	for i := 0; i < 45; i++ {
		fmt.Fprintf(&cargo, "   Compiling crate-%02d v0.%d.0\n", i, i%9)
	}
	cargo.WriteString("    Finished `dev` profile [unoptimized + debuginfo] target(s) in 14.02s\n")

	var tfplan strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&tfplan, "aws_iam_role.svc%02d: Refreshing state... [id=svc%02d]\ndata.aws_policy.p%02d: Reading...\ndata.aws_policy.p%02d: Read complete after 0s\n", i, i, i, i)
	}
	tfplan.WriteString("\nTerraform will perform the following actions:\n\n  # aws_s3_bucket.logs will be created\n  + resource \"aws_s3_bucket\" \"logs\" {\n      + bucket = \"my-logs\"\n    }\n\nPlan: 1 to add, 0 to change, 0 to destroy.\n")

	var pip strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&pip, "Collecting package-%02d\n  Downloading package_%02d-1.%d.0-py3-none-any.whl (%d kB)\n", i, i, i%7, i*13+40)
	}
	pip.WriteString("Installing collected packages: many\nSuccessfully installed all-the-things-1.0\n")

	var dbuild strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&dbuild, "#%d [%d/30] RUN step-%d\n#%d DONE %d.%ds\n", i, i, i, i, i%9, i%10)
	}

	var grep strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&grep, "internal/pkg%02d/file%02d.go:%d:\tif err := doThing%d(ctx); err != nil {\n", i%20, i%7, i*3+1, i)
	}

	var curl strings.Builder
	curl.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head><title>Docs</title></head>\n<body>\n")
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&curl, "<p class=\"doc-line\">Documentation paragraph %d with some explanatory prose in it.</p>\n", i)
	}
	curl.WriteString("</body>\n</html>\n")

	var ghlog strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&ghlog, "test (ubuntu-latest)\tRun go test ./...\t2026-07-10T21:13:%02d.%07dZ ok  \tgithub.com/x/pkg%03d\t0.%02ds\n", i%60, i*13%9999999, i, i%99)
	}

	var sed strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sed, "line %03d of the printed file with enough width to look like source code\n", i)
	}

	var ls strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&ls, "-rw-r--r--   1 dev  staff  %5d Jul 10 12:%02d file_%03d.go\n", i*137, i%60, i)
	}

	var find strings.Builder
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&find, "./internal/pkg%02d/subpkg/file_%03d_test.go\n", i%25, i)
	}

	var tree strings.Builder
	tree.WriteString(".\n")
	for i := 0; i < 350; i++ {
		fmt.Fprintf(&tree, "│   ├── file_%03d.go\n", i)
	}
	tree.WriteString("\n25 directories, 350 files\n")

	var rg strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&rg, "internal/pkg%02d/file%02d.go:%d:\tif err := doThing%d(ctx); err != nil {\n", i%20, i%7, i*3+1, i)
	}

	var pdiff strings.Builder
	pdiff.WriteString("--- a/config.yaml\n+++ b/config.yaml\n")
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&pdiff, "@@ -%d,3 +%d,3 @@\n key_%03d: stable\n-value_%03d: old\n+value_%03d: new\n", i*4+1, i*4+1, i, i, i)
	}

	var dps strings.Builder
	dps.WriteString("CONTAINER ID   IMAGE          COMMAND                  CREATED      STATUS      PORTS                    NAMES\n")
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&dps, "%012x   svc-%03d:1.2    \"/entrypoint.sh run\"     2 days ago   Up 2 days   0.0.0.0:%d->8080/tcp   svc-%03d\n", i*7919, i, 30000+i, i)
	}

	var ocGet strings.Builder
	ocGet.WriteString("NAME                     READY   STATUS    RESTARTS   AGE\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&ocGet, "pod-%03d-5c8b7d6f4-p9q%d  1/1     Running   %d          5h\n", i, i%10, i%4)
	}

	var ec2 strings.Builder
	ec2.WriteString("{\n    \"Reservations\": [\n")
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&ec2, "        {\n            \"Instances\": [\n                {\n                    \"InstanceId\": \"i-0abc%012d\",\n                    \"InstanceType\": \"t3.medium\",\n                    \"State\": { \"Code\": 16, \"Name\": \"running\" },\n", i)
		for j := 0; j < 80; j++ {
			fmt.Fprintf(&ec2, "                    \"Detail%02d\": \"value-%d-%d\",\n", j, i, j)
		}
		ec2.WriteString("                    \"Hypervisor\": \"nitro\"\n                }\n            ]\n        },\n")
	}
	ec2.WriteString("    ]\n}\n")

	var awsLogs strings.Builder
	awsLogs.WriteString("{\n    \"events\": [\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&awsLogs, "        {\n            \"timestamp\": 17526%06d,\n            \"message\": \"GET /api/items/%d 200 in %dms\",\n            \"ingestionTime\": 17526%06d\n        },\n", i, i, i%90+3, i+450)
	}
	awsLogs.WriteString("    ],\n    \"nextForwardToken\": \"f/383912345678901234567890\"\n}\n")

	var s3ls strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&s3ls, "2026-07-%02d 12:%02d:%02d    %8d backups/db-part-%04d.tar.gz\n", i%28+1, i%60, i%60, i*4096+1024, i)
	}

	var bundle strings.Builder
	bundle.WriteString("Fetching gem metadata from https://rubygems.org/.........\nResolving dependencies...\n")
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&bundle, "Fetching gem-%03d 1.%d.0\nInstalling gem-%03d 1.%d.0\n", i, i%9, i, i%9)
	}
	bundle.WriteString("Bundle complete! 42 Gemfile dependencies, 150 gems now installed.\n")

	return []corpusCase{
		{"go test ./...", goTest.String(), 85},
		{"pytest", pytest.String(), 75},
		{"git status", gitStatus.String(), 35},
		{"npm install", npm.String(), 85},
		{"git push origin main", push.String(), 70},
		{"npx jest", jest.String(), 85},
		{"cargo build", cargo.String(), 90},
		{"terraform plan", tfplan.String(), 75},
		{"pip3 install -r requirements.txt", pip.String(), 85},
		{"docker build -t app .", dbuild.String(), 90},
		{"grep -rn 'err !=' internal/", grep.String(), 55},
		{"curl -s https://example.com/docs", curl.String(), 50},
		{"gh run view 29123153293 --log", ghlog.String(), 55},
		{"sed -n '1,400p' main.go", sed.String(), 55},
		{"ls -la internal/", ls.String(), 50},
		{"find . -name '*_test.go'", find.String(), 65},
		{"tree internal/", tree.String(), 60},
		{"rg 'err !=' internal/", rg.String(), 55},
		{"diff -u a/config.yaml b/config.yaml", pdiff.String(), 65},
		{"docker ps -a", dps.String(), 50},
		{"oc get pods", ocGet.String(), 60},
		{"aws ec2 describe-instances", ec2.String(), 75},
		{"aws logs get-log-events --log-group-name app", awsLogs.String(), 70},
		{"aws s3 ls s3://backups --recursive", s3ls.String(), 55},
		{"bundle install", bundle.String(), 90},
	}
}

// TestCorpusSavings guards the headline claim: built-in filters must save
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
