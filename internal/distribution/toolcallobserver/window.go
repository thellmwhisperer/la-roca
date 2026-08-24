package toolcallobserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"
)

type WindowKind string

const (
	WindowHerdr    WindowKind = "herdr"
	WindowTerminal WindowKind = "terminal"
	// HumanWorkspaceLabel is the operator's designated human-facing Herdr workspace.
	HumanWorkspaceLabel = "\x63\x61\x70\x74\x61\x69\x6e"
	observerLabel       = "tool call observer"
	herdrName           = "herdr"
)

type Workspace struct {
	ID      string `json:"workspace_id"`
	Label   string `json:"label"`
	Focused bool   `json:"focused"`
}

type OpenedWindow struct {
	Kind      WindowKind `json:"kind"`
	Workspace string     `json:"workspace,omitempty"`
	TabID     string     `json:"tab_id,omitempty"`
}

type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type TerminalRequest struct {
	Command []string
	Env     []string
	Cwd     string
}

type WindowRequest struct {
	Runner       CommandRunner
	LookPath     func(string) (string, bool)
	OpenTerminal func(TerminalRequest) error
	Command      []string
	Env          []string
	Cwd          string
	CurrentID    string
}

func ChooseWorkspace(workspaces []Workspace, currentID string) (Workspace, string) {
	for _, workspace := range workspaces {
		if workspace.Label == HumanWorkspaceLabel {
			return workspace, HumanWorkspaceLabel
		}
	}
	if currentID != "" {
		for _, workspace := range workspaces {
			if workspace.ID == currentID {
				return workspace, "current"
			}
		}
	}
	for _, workspace := range workspaces {
		if workspace.Focused {
			return workspace, "current"
		}
	}
	return Workspace{}, ""
}

func OpenWindow(ctx context.Context, session Session, req WindowRequest) (OpenedWindow, error) {
	look := req.LookPath
	if look == nil {
		look = lookOnPath
	}
	if _, ok := look(herdrName); ok {
		if opened, err := openHerdrWindow(ctx, req); err == nil {
			return opened, nil
		}
	}
	open := req.OpenTerminal
	if open == nil {
		open = openOSTerminal
	}
	if err := open(TerminalRequest{Command: req.Command, Env: req.Env, Cwd: req.Cwd}); err != nil {
		return OpenedWindow{}, fmt.Errorf("cannot open a live observer window: %w", err)
	}
	return OpenedWindow{Kind: WindowTerminal}, nil
}

func openHerdrWindow(ctx context.Context, req WindowRequest) (OpenedWindow, error) {
	run := req.Runner
	if run == nil {
		run = runCommand
	}
	probe, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	listed, err := run(probe, herdrName, "workspace", "list")
	if err != nil {
		return OpenedWindow{}, err
	}
	workspaces, err := parseWorkspaces(listed)
	if err != nil {
		return OpenedWindow{}, err
	}
	workspace, reason := ChooseWorkspace(workspaces, req.CurrentID)
	if workspace.ID == "" {
		return OpenedWindow{}, fmt.Errorf("Herdr listed no workspace to open")
	}
	args := []string{"tab", "create", "--workspace", workspace.ID, "--label", observerLabel, "--focus"}
	if req.Cwd != "" {
		args = append(args, "--cwd", req.Cwd)
	}
	for _, env := range req.Env {
		args = append(args, "--env", env)
	}
	created, err := run(ctx, herdrName, args...)
	if err != nil {
		return OpenedWindow{}, err
	}
	tabID, paneID, err := parseTabCreate(created)
	if err != nil {
		return OpenedWindow{}, err
	}
	if paneID != "" && len(req.Command) > 0 {
		runArgs := append([]string{"pane", "run", paneID}, req.Command...)
		if _, err := run(ctx, herdrName, runArgs...); err != nil {
			return OpenedWindow{}, err
		}
	}
	return OpenedWindow{Kind: WindowHerdr, Workspace: reason, TabID: tabID}, nil
}

func parseWorkspaces(raw []byte) ([]Workspace, error) {
	var envelope struct {
		Result struct {
			Workspaces []Workspace `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("Herdr workspace list was not JSON: %w", err)
	}
	return envelope.Result.Workspaces, nil
}

func parseTabCreate(raw []byte) (tabID, paneID string, err error) {
	var envelope struct {
		Result struct {
			Tab struct {
				TabID string `json:"tab_id"`
			} `json:"tab"`
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", "", fmt.Errorf("Herdr tab create was not JSON: %w", err)
	}
	if envelope.Result.Tab.TabID == "" {
		return "", "", fmt.Errorf("Herdr tab create did not return a tab id")
	}
	if envelope.Result.RootPane.PaneID == "" {
		return "", "", fmt.Errorf("Herdr tab create did not return a root pane id")
	}
	return envelope.Result.Tab.TabID, envelope.Result.RootPane.PaneID, nil
}

func lookOnPath(name string) (string, bool) {
	path, err := exec.LookPath(name)
	return path, err == nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
