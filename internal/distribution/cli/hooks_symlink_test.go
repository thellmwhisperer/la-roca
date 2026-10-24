package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An operator who keeps Claude settings in a dotfiles repository links them
// into ~/.claude. Every hook edit lands in the file the link names, and the
// link is still a link afterwards.
func TestClaudeHooksEditTheTargetOfASymlinkedSettingsPath(t *testing.T) {
	home, binary, path := claudeHookHome(t)
	target := filepath.Join(home, "dotfiles", "claude.json")
	initial := `{"permissions":{"allow":["Read"]}}`
	writeFile(t, target, initial)
	linkClaudeSettings(t, path, target)

	first, err := installClaudeAuthorshipHook(path, binary)
	if err != nil || !first.Changed {
		t.Fatalf("install through the link: changed=%v err=%v", first.Changed, err)
	}
	if backup, err := os.ReadFile(first.Backup); err != nil || string(backup) != initial {
		t.Errorf("install backup %q = %q, %v; want the settings it replaced", first.Backup, backup, err)
	}
	assertClaudeSettingsLink(t, path, target)
	if again, err := installClaudeAuthorshipHook(path, binary); err != nil || again.Changed {
		t.Fatalf("install is not idempotent through the link: changed=%v err=%v", again.Changed, err)
	}
	body := readSettings(t, target)
	if !strings.Contains(body, `"permissions"`) ||
		strings.Count(body, claudeHookCommand(binary)) != 1 {
		t.Fatalf("install did not edit the linked settings once: %s", body)
	}

	var output strings.Builder
	runHookCLI(t, &output, nil, "install", "claude", "--pills")
	assertClaudeSettingsLink(t, path, target)
	settings := readClaudeHookSettings(t, target)
	assertHookCommand(t, settings.Hooks["SessionStart"], "",
		sessionHookCommand(binary, "claude", sessionRequest{pills: true}), 1)
	assertHookCommand(t, settings.Hooks["PreToolUse"], "Bash", claudeHookCommand(binary), 1)

	runHookCLI(t, &output, nil, "uninstall", "claude")
	assertClaudeSettingsLink(t, path, target)
	body = readSettings(t, target)
	if strings.Contains(body, "hooks run") || !strings.Contains(body, `"permissions"`) {
		t.Fatalf("uninstall did not withdraw exactly its hooks from the linked settings: %s", body)
	}
	if again, _, err := uninstallClaudeAuthorshipHook(path); err != nil || again.Changed {
		t.Fatalf("uninstall is not idempotent through the link: changed=%v err=%v", again.Changed, err)
	}
	assertClaudeSettingsLink(t, path, target)
}

func TestClaudeHooksRefuseASymlinkWithNoRegularTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(t *testing.T, target string)
	}{
		{"broken", func(*testing.T, string) {}},
		{"directory", func(t *testing.T, target string) {
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, binary, path := claudeHookHome(t)
			target := filepath.Join(home, "dotfiles", "claude.json")
			test.create(t, target)
			linkClaudeSettings(t, path, target)
			before, beforeErr := os.Lstat(target)

			if _, err := installClaudeAuthorshipHook(path, binary); err == nil {
				t.Error("install accepted a settings link with no regular target")
			}
			if _, _, err := uninstallClaudeAuthorshipHook(path); err == nil {
				t.Error("uninstall accepted a settings link with no regular target")
			}
			assertClaudeSettingsLink(t, path, target)
			after, afterErr := os.Lstat(target)
			if (beforeErr == nil) != (afterErr == nil) ||
				(beforeErr == nil && (!os.SameFile(before, after) || before.Mode() != after.Mode())) {
				t.Fatalf("refused edit changed the link target: before=%v,%v after=%v,%v",
					before, beforeErr, after, afterErr)
			}
			if entries, err := os.ReadDir(filepath.Dir(path)); err != nil || len(entries) != 1 {
				t.Fatalf("refused edit left files beside the link: %v, %v", entries, err)
			}
		})
	}
}

func linkClaudeSettings(t *testing.T, path, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func assertClaudeSettingsLink(t *testing.T, path, target string) {
	t.Helper()
	if got, err := os.Readlink(path); err != nil || got != target {
		t.Fatalf("settings link = %q, %v; want a link to %q", got, err, target)
	}
}
