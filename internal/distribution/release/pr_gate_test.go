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
	for _, test := range []struct {
		name string
		body string
		pass bool
	}{
		{"comment alone cannot satisfy acceptance", body, false},
		{"body edit supplies acceptance", body + evidence, true},
		{"removing evidence fails again", body, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TEST_PR_BODY", test.body)
			cmd := exec.Command("bash", "../../../scripts/pr-gate.sh")
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.pass {
				t.Fatalf("pass=%t, error=%v\n%s", test.pass, err, output)
			}
			if !test.pass && !strings.Contains(string(output), "editing the body reruns this check") {
				t.Fatalf("missing executable next step:\n%s", output)
			}
		})
	}
}

func TestPRGateHighRiskAcceptanceRequiresOwnerActor(t *testing.T) {
	bin := t.TempDir()
	fakeGH := `#!/bin/sh
case "$*" in
  *--json\ body*) printf '%b\n' '## Risk Assessment\nHigh' ;;
  *--json\ author*) printf '%s\n' author ;;
  *--json\ labels*) printf '%s\n' 'risk:accepted' ;;
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
	t.Setenv("PR_GATE_ACTION", "labeled")
	t.Setenv("PR_GATE_REVIEWER", "owner")
	for _, test := range []struct {
		name   string
		actor  string
		label  string
		passed bool
	}{
		{name: "owner accepts risk", actor: "owner", label: "risk:accepted", passed: true},
		{name: "collaborator cannot accept risk", actor: "collaborator", label: "risk:accepted", passed: false},
		{name: "owner cannot accept unrelated label", actor: "owner", label: "documentation", passed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GITHUB_ACTOR", test.actor)
			t.Setenv("PR_GATE_LABEL_NAME", test.label)
			cmd := exec.Command("bash", "../../../scripts/pr-gate.sh")
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.passed {
				t.Fatalf("passed=%t, error=%v\n%s", test.passed, err, output)
			}
		})
	}
}
