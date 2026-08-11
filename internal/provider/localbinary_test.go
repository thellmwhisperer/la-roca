package provider

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake shell binary is only used on Unix")
	}
	path := filepath.Join(t.TempDir(), "fake-provider")
	script := `#!/bin/sh
case "$FAKE_PROVIDER_MODE" in
  json) printf '%s' '{"result":"SELECT 1"}' ;;
  malformed) printf '%s' '{not-json' ;;
  failure) printf '%s' 'synthetic failure' >&2; exit 7 ;;
  timeout) sleep 30 ;;
  oversized) dd if=/dev/zero bs=2097152 count=1 2>/dev/null ;;
  arguments) printf '%s' "$*" ;;
  *) printf '%s' 'plain answer' ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalBinaryAnswersPlainTextAndJSON(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		command    []string
		format     string
		want       string
	}{
		{name: "plain stdout", command: []string{"binary", "{prompt}", "--model", "{model}"}, want: "plain answer"},
		{name: "JSON input flag with plain stdout", command: []string{"binary", "--input-format", "json"}, want: "plain answer"},
		{name: "JSON result envelope", mode: "json", format: "json", command: []string{"binary", "-p", "--output-format", "json", "--model", "{model}"}, want: "SELECT 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_PROVIDER_MODE", tc.mode)
			command := append([]string(nil), tc.command...)
			command[0] = fakeBinary(t)
			binary, err := NewLocalBinary(LocalBinaryConfig{
				Name: "fixture", Command: command, Model: "fixture-model",
				Variables: map[string]string{"model": "fixture-model"}, WorkDir: t.TempDir(), ResponseFormat: tc.format,
			})
			if err != nil {
				t.Fatal(err)
			}
			answer, err := binary.Chat(t.Context(), ChatRequest{Messages: []Message{{Role: RoleUser, Content: "question"}}})
			if err != nil || answer.Content != tc.want {
				t.Fatalf("answer = %+v, err = %v, want %q", answer, err, tc.want)
			}
		})
	}
}

func TestLocalBinaryFailuresAreBoundedAndNamed(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		timeout    time.Duration
		want       string
	}{
		{name: "non-zero exit", mode: "failure", want: "synthetic failure"},
		{name: "malformed JSON", mode: "malformed", want: "valid JSON"},
		{name: "timeout", mode: "timeout", timeout: 30 * time.Millisecond, want: "timed out"},
		{name: "output limit", mode: "oversized", want: "output limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_PROVIDER_MODE", tc.mode)
			binary, err := NewLocalBinary(LocalBinaryConfig{
				Name: "fixture", Command: []string{fakeBinary(t), "--output-format", "json"},
				Model: "fixture-model", Variables: map[string]string{"model": "fixture-model"},
				WorkDir: t.TempDir(), Timeout: tc.timeout, ResponseFormat: "json",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = binary.Chat(context.Background(), ChatRequest{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestLocalBinaryRejectsAnUnknownResponseFormatByName(t *testing.T) {
	_, err := NewLocalBinary(LocalBinaryConfig{
		Name: "fixture", Command: []string{"fixture"}, ResponseFormat: "yaml",
		File: "/synthetic/config.toml", WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("unknown response format was accepted")
	}
	for _, piece := range []string{"fixture", "response_format", "yaml", "/synthetic/config.toml"} {
		if !strings.Contains(err.Error(), piece) {
			t.Errorf("error does not name %q: %v", piece, err)
		}
	}
}

func TestLocalBinarySubstitutesEveryDeclaredProviderValue(t *testing.T) {
	t.Setenv("FAKE_PROVIDER_MODE", "arguments")
	binary, err := NewLocalBinary(LocalBinaryConfig{
		Name: "fixture", Command: []string{fakeBinary(t), "--effort", "{effort}", "--thinking={thinking}"},
		Variables: map[string]string{"effort": "high", "thinking": "false"}, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := binary.Chat(t.Context(), ChatRequest{})
	if err != nil || answer.Content != "--effort high --thinking=false" {
		t.Fatalf("answer = %+v, err = %v", answer, err)
	}
}

func TestLocalBinaryRejectsAnUnknownPlaceholderByName(t *testing.T) {
	_, err := NewLocalBinary(LocalBinaryConfig{
		Name: "fixture", Command: []string{"fixture", "{effort}"},
		File: "/synthetic/config.toml", WorkDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("unknown placeholder was accepted")
	}
	for _, piece := range []string{"fixture", "{effort}", "/synthetic/config.toml"} {
		if !strings.Contains(err.Error(), piece) {
			t.Errorf("error does not name %q: %v", piece, err)
		}
	}
}

func TestLocalBinaryReportsAMissingExecutable(t *testing.T) {
	binary, err := NewLocalBinary(LocalBinaryConfig{
		Name: "fixture", Command: []string{"roca-definitely-absent-binary"},
		Model: "fixture-model", WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := binary.Ready(t.Context())
	if ready.Ready || !strings.Contains(ready.Reason, "not found") || !strings.Contains(ready.Action, "PATH") {
		t.Fatalf("readiness = %+v", ready)
	}
}
