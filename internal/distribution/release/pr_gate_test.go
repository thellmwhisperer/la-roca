package release

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPRGateAcceptanceBodyEdits(t *testing.T) {
	bin := t.TempDir()
	fakeGH := `#!/bin/sh
case "$*" in
  *--json\ baseRefName*) printf '%s\n' "$TEST_PR_BASE" ;;
  *--json\ headRefName*) printf '%s\n' "$TEST_PR_HEAD" ;;
  *--json\ headRepository*) printf '%s\n' "$TEST_PR_HEAD_REPO" ;;
  *--json\ body*) printf '%s\n' "$TEST_PR_BODY" ;;
  *--json\ author*) printf '%s\n' author ;;
  *--json\ labels*) ;;
  *issues/1/comments*) printf '%s\n' "$TEST_PR_COMMENT" ;;
  "api repos/fixture/repo --jq .owner.login") printf '%s\n' owner ;;
  *) printf 'unexpected gh call: %s\n' "$*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GITHUB_REPOSITORY", "fixture/repo")
	t.Setenv("PR_NUMBER", "1")
	t.Setenv("PR_GATE_FORK", "true")
	t.Setenv("GITHUB_STEP_SUMMARY", "")
	body := "## Risk Assessment\nMedium\n"
	evidence := "\n## Acceptance\n\n```sh\n$ roca --version\nroca fixture\n```\n"
	t.Setenv("TEST_PR_COMMENT", evidence)
	t.Setenv("TEST_PR_BASE", "main")
	t.Setenv("TEST_PR_HEAD", "feature")
	t.Setenv("TEST_PR_HEAD_REPO", "fixture/repo")
	for _, test := range []struct {
		name, body, base, head, headRepo string
		pass                             bool
	}{
		{"comment alone cannot satisfy acceptance", body, "main", "feature", "fixture/repo", false},
		{"body edit supplies acceptance", body + evidence, "main", "feature", "fixture/repo", true},
		{"removing evidence fails again", body, "main", "feature", "fixture/repo", false},
		{"unrecognized PR remains gated", "", "main", "feature", "fixture/repo", false},
		{"release-please wrong direction remains gated", "", "integration", "release-please--branches--main--components--roca", "fixture/repo", false},
		{"hotfix wrong direction remains gated", "", "integration", "hotfix/fix-outage", "fixture/repo", false},
		{"fork cannot impersonate release-please", "", "main", "release-please--branches--main--components--roca", "contributor/repo", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TEST_PR_BODY", test.body)
			t.Setenv("TEST_PR_BASE", test.base)
			t.Setenv("TEST_PR_HEAD", test.head)
			t.Setenv("TEST_PR_HEAD_REPO", test.headRepo)
			cmd := exec.Command("bash", "../../../scripts/pr-gate.sh")
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.pass {
				t.Fatalf("pass=%t, error=%v\n%s", test.pass, err, output)
			}
			if !test.pass && !strings.Contains(string(output), "Risk Assessment is High") &&
				!strings.Contains(string(output), "editing the body reruns this check") {
				t.Fatalf("unrecognized PR skipped the existing gate:\n%s", output)
			}
		})
	}

	for _, test := range []struct {
		name, base, head, step string
	}{
		{"back-merge", "integration", "main", "back-merge main -> integration"},
		{"release", "main", "integration", "release integration -> main"},
		{"release-please", "main", "release-please--branches--main--components--roca", "release-please"},
		{"hotfix", "main", "hotfix/fix-outage", "hotfix"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TEST_PR_BASE", test.base)
			t.Setenv("TEST_PR_HEAD", test.head)
			t.Setenv("TEST_PR_HEAD_REPO", "fixture/repo")
			t.Setenv("TEST_PR_BODY", "")
			cmd := exec.Command("bash", "../../../scripts/pr-gate.sh")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("recognized step blocked: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "recognized release-train step: "+test.step) {
				t.Fatalf("missing step log %q:\n%s", test.step, output)
			}
		})
	}
}
