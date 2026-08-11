package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

const DefaultBinaryTimeout = 120 * time.Second

const (
	commandOutputBudget = 1 << 20
	commandErrorBudget  = 64 << 10
	binaryResponseText  = "text"
	binaryResponseJSON  = "json"
)

type LocalBinaryConfig struct {
	Name    string
	Command []string
	Model   string
	Models  []string
	// Variables are provider-table scalars available to every argv element.
	Variables map[string]string
	File      string
	WorkDir   string
	Timeout   time.Duration
	Action    string
	// ResponseFormat declares how stdout is decoded. Empty means plain text.
	ResponseFormat string
}

type LocalBinary struct {
	name           string
	command        []string
	model          string
	models         []string
	variables      map[string]string
	workDir        string
	timeout        time.Duration
	action         string
	responseFormat string
}

func NewLocalBinary(cfg LocalBinaryConfig) (*LocalBinary, error) {
	name := normalize(cfg.Name)
	if name == "" {
		return nil, fmt.Errorf("a local-binary provider needs a name")
	}
	if len(cfg.Command) == 0 || strings.TrimSpace(cfg.Command[0]) == "" {
		return nil, fmt.Errorf("provider %s needs a non-empty command", name)
	}
	if strings.TrimSpace(cfg.WorkDir) == "" {
		return nil, fmt.Errorf("provider %s needs La Roca's dedicated runner directory", name)
	}
	responseFormat := strings.ToLower(strings.TrimSpace(cfg.ResponseFormat))
	if responseFormat == "" {
		responseFormat = binaryResponseText
	}
	if responseFormat != binaryResponseText && responseFormat != binaryResponseJSON {
		location := cfg.File
		if location == "" {
			location = "the provider configuration"
		}
		return nil, fmt.Errorf("provider %q in %s has unknown response_format %q; use text or json",
			name, location, cfg.ResponseFormat)
	}
	for _, placeholder := range config.CommandPlaceholders(cfg.Command) {
		if placeholder == "prompt" {
			continue
		}
		if _, exists := cfg.Variables[placeholder]; !exists {
			location := cfg.File
			if location == "" {
				location = "the provider configuration"
			}
			return nil, fmt.Errorf(
				"provider %q command in %s uses unknown placeholder {%s}", name, location, placeholder)
		}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultBinaryTimeout
	}
	variables := make(map[string]string, len(cfg.Variables))
	for key, value := range cfg.Variables {
		variables[key] = value
	}
	action := cfg.Action
	if action == "" {
		executable := renderCommandValue(cfg.Command[0], "", variables)
		action = fmt.Sprintf("install %s or put %s on PATH, or fix its command in the configuration",
			filepath.Base(executable), filepath.Base(executable))
	}
	return &LocalBinary{
		name: name, command: append([]string(nil), cfg.Command...), model: cfg.Model,
		models: append([]string(nil), cfg.Models...), variables: variables,
		workDir: cfg.WorkDir, timeout: timeout, action: action, responseFormat: responseFormat,
	}, nil
}

func (b *LocalBinary) Name() string { return b.name }

func (b *LocalBinary) ModelID() string { return b.model }

func (b *LocalBinary) RequestTimeout() time.Duration { return b.timeout }

func (b *LocalBinary) ExternalCredential() bool { return true }

func (b *LocalBinary) ModelChoices() []string {
	choices := append([]string(nil), b.models...)
	if b.model != "" {
		choices = append([]string{b.model}, choices...)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(choices))
	for _, choice := range choices {
		if choice != "" && !seen[choice] {
			seen[choice] = true
			out = append(out, choice)
		}
	}
	return out
}

func (b *LocalBinary) Ready(context.Context) Readiness {
	command, _ := b.renderCommand("")
	if _, err := executable(command[0]); err != nil {
		return Readiness{ModelID: b.model,
			Reason: fmt.Sprintf("%s binary not found in PATH", filepath.Base(command[0])),
			Action: b.action}
	}
	return Readiness{Ready: true, ModelID: b.model}
}

func (b *LocalBinary) DiagnoseReady(ctx context.Context) Readiness {
	ready := b.Ready(ctx)
	if !ready.Ready {
		return ready
	}
	if err := ProbeModel(ctx, b); err != nil {
		return Readiness{ModelID: b.model,
			Reason: fmt.Sprintf("%s account probe failed: %v", b.name, err),
			Action: "sign in with the local CLI, then run `roca login " + b.name + "` again"}
	}
	return ready
}

func (b *LocalBinary) Models(ctx context.Context) ModelReport {
	ready := b.DiagnoseReady(ctx)
	if !ready.Ready {
		return ModelReport{Reason: ready.Reason}
	}
	return ModelReport{Ready: true, Models: b.ModelChoices()}
}

func (b *LocalBinary) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()

	prompt := binaryPrompt(req.Messages)
	command, promptInArgs := b.renderCommand(prompt)
	executablePath, err := executable(command[0])
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ask %s: binary %q was not found: %w", b.name, command[0], err)
	}
	if err := os.MkdirAll(b.workDir, 0o700); err != nil {
		return ChatResponse{}, fmt.Errorf("prepare %s runner directory: %w", b.name, err)
	}
	if err := os.Chmod(b.workDir, 0o700); err != nil {
		return ChatResponse{}, fmt.Errorf("protect %s runner directory: %w", b.name, err)
	}

	args := command[1:]
	cmd := exec.CommandContext(callCtx, executablePath, args...)
	cmd.Dir = b.workDir
	cmd.WaitDelay = time.Second
	if !promptInArgs {
		cmd.Stdin = strings.NewReader(prompt)
	}
	stdout := cappedBuffer{limit: commandOutputBudget}
	stderr := cappedBuffer{limit: commandErrorBudget}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = runLocalCommand(cmd)
	if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
		return ChatResponse{}, fmt.Errorf("%s command timed out: %w", b.name, callCtx.Err())
	}
	if callCtx.Err() != nil {
		return ChatResponse{}, fmt.Errorf("%s command was canceled: %w", b.name, callCtx.Err())
	}
	if stdout.exceeded {
		return ChatResponse{}, fmt.Errorf("%s command stdout exceeded the %d-byte output limit",
			b.name, commandOutputBudget)
	}
	if stderr.exceeded {
		return ChatResponse{}, fmt.Errorf("%s command stderr exceeded the %d-byte output limit",
			b.name, commandErrorBudget)
	}
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return ChatResponse{}, fmt.Errorf("%s command failed: %s", b.name, truncateCommandError(detail))
	}

	content := strings.TrimSpace(stdout.String())
	if b.responseFormat == binaryResponseJSON {
		var envelope struct {
			Result string `json:"result"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			return ChatResponse{}, fmt.Errorf("%s command did not return valid JSON: %w", b.name, err)
		}
		content = strings.TrimSpace(envelope.Result)
		if content == "" {
			return ChatResponse{}, fmt.Errorf("%s command JSON has no result text", b.name)
		}
	}
	if content == "" {
		return ChatResponse{}, fmt.Errorf("%s command returned an empty answer", b.name)
	}
	return ChatResponse{Content: content, Provider: b.name, ModelID: b.model}, nil
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = b.exceeded || written > 0
		return written, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.exceeded = true
	}
	_, _ = b.buffer.Write(p)
	return written, nil
}

func (b *cappedBuffer) String() string { return b.buffer.String() }

func (b *cappedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func executable(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Abs(path)
}

func (b *LocalBinary) renderCommand(prompt string) ([]string, bool) {
	command := make([]string, 0, len(b.command))
	promptInArgs := false
	for _, raw := range b.command {
		if strings.Contains(raw, "{prompt}") {
			promptInArgs = true
		}
		command = append(command, renderCommandValue(raw, prompt, b.variables))
	}
	return command, promptInArgs
}

func renderCommandValue(value, prompt string, variables map[string]string) string {
	for _, placeholder := range config.CommandPlaceholders([]string{value}) {
		replacement := prompt
		if placeholder != "prompt" {
			replacement = variables[placeholder]
		}
		value = strings.ReplaceAll(value, "{"+placeholder+"}", replacement)
	}
	return value
}

func binaryPrompt(messages []Message) string {
	var prompt strings.Builder
	for _, message := range messages {
		role := strings.ToUpper(strings.TrimSpace(message.Role))
		if role == "" {
			role = "USER"
		}
		fmt.Fprintf(&prompt, "%s:\n%s\n\n", role, message.Content)
	}
	return strings.TrimSpace(prompt.String())
}

func truncateCommandError(message string) string {
	if len(message) <= errorBodyBudget {
		return message
	}
	return message[:errorBodyBudget] + "..."
}
