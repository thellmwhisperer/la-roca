// Package reconcile notices capabilities a newer binary can use but the
// operator's existing configuration does not yet request.
package reconcile

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/provider"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	ProposalClaudeCLI       = "claude-cli-provider"
	ProposalCodexCLI        = "codex-cli-transport"
	ProposalAnthropicExport = "anthropic-export-path"

	CapabilityAnthropicExport = "anthropic-export-ingester"
	inputValue                = "{operator-input}"
)

type ProviderCondition string

const (
	ProviderUnavailable ProviderCondition = "unavailable"
	ProviderHTTP        ProviderCondition = "http"
)

// Detection is an AND of observable environment and configuration facts.
// Empty fields impose no condition.
type Detection struct {
	Capability       string
	Binary           string
	Provider         string
	ProviderState    ProviderCondition
	Credential       string
	DefaultListEmpty string
}

// Proposal is everything the generic runner needs to present and apply an
// entry. Changes are the exact TOML operations accepting it performs.
type Proposal struct {
	Alert       string
	Prompt      string
	InputPrompt string
	Changes     []config.Change
}

type Entry struct {
	ID        string
	Detection Detection
	Proposal  Proposal
}

type Context struct {
	Version         string
	ConfigPath      string
	StampPath       string
	CredentialsPath string
	LookPath        func(string) (string, error)
	Env             func(string) string
	File            config.File
	Capabilities    map[string]bool
}

type Options struct {
	Interactive bool
	ListAll     bool
	In          io.Reader
	Out         io.Writer
}

type Result struct {
	Pending  int
	Offered  int
	Accepted int
	Changes  []agentcfg.Outcome
}

// Registry is the launch catalogue. Feature-specific facts and writes stop at
// this table; Open and Run are generic over Entry.
func Registry() []Entry {
	return []Entry{
		{
			ID: ProposalClaudeCLI,
			Detection: Detection{Binary: provider.NameClaude, Provider: provider.NameClaude,
				ProviderState: ProviderUnavailable},
			Proposal: Proposal{
				Alert:  "Claude Code is on PATH but no usable Claude provider is configured; model sonnet can answer through the existing local CLI session.",
				Prompt: "Enable the Claude provider?",
				Changes: []config.Change{{Kind: config.PrependUnique, Table: "models", Key: "order",
					Value: provider.NameClaude, Default: provider.DefaultOrder()},
					{Kind: config.ReplaceTable, Table: "models.claude"}},
			},
		},
		{
			ID: ProposalCodexCLI,
			Detection: Detection{Binary: provider.NameCodex, Provider: provider.NameCodex,
				ProviderState: ProviderHTTP, Credential: provider.FileCodexSession},
			Proposal: Proposal{
				Alert:  "Codex is using La Roca's OAuth/HTTP session while the Codex CLI is on PATH; the local-binary transport uses the same subscription without token refresh.",
				Prompt: "Switch Codex to the local-binary transport?",
				Changes: []config.Change{{Kind: config.ReplaceTable, Table: "models.codex",
					Fields: []config.Field{
						{Key: "command", Value: []string{"codex", "exec", "--model", "{model}",
							"--sandbox", "read-only", "--ephemeral", "--skip-git-repo-check",
							"--ignore-user-config", "--ignore-rules", "--color", "never", "-"}},
						{Key: "model", ValueFrom: "models.codex.model", Fallback: provider.DefaultCodexModel},
					}}},
			},
		},
		{
			ID: ProposalAnthropicExport,
			Detection: Detection{Capability: CapabilityAnthropicExport,
				DefaultListEmpty: "anthropic_export_paths"},
			Proposal: Proposal{
				Alert:  "Anthropic export ingest is available but defaults.anthropic_export_paths is empty; point it at an extracted export folder (docs: https://github.com/thellmwhisperer/la-roca/blob/main/docs/ingest.md#declare-an-anthropic-data-export).",
				Prompt: "Add an Anthropic export folder?", InputPrompt: "Export folder: ",
				Changes: []config.Change{{Kind: config.SetValue, Table: "defaults",
					Key: "anthropic_export_paths", Value: []string{inputValue}}},
			},
		},
	}
}

func Open(context Context, registry []Entry) []Entry {
	file := context.File
	if file.Path == "" && context.ConfigPath != "" {
		loaded, err := config.LoadFile(context.ConfigPath)
		if err != nil {
			return nil
		}
		file = loaded
	}
	var open []Entry
	for _, entry := range registry {
		if detected(context, file, entry.Detection) {
			open = append(open, entry)
		}
	}
	return open
}

func detected(context Context, file config.File, detection Detection) bool {
	if detection.Capability != "" && !context.Capabilities[detection.Capability] {
		return false
	}
	if detection.Binary != "" && !binaryOnPath(context, detection.Binary) {
		return false
	}
	if detection.Credential != "" && !regularFile(filepath.Join(context.CredentialsPath, detection.Credential)) {
		return false
	}
	if detection.DefaultListEmpty != "" && len(file.DefaultList(detection.DefaultListEmpty)) > 0 {
		return false
	}
	if detection.Provider != "" {
		order := file.Models.Order
		if order == nil {
			order = provider.DefaultOrder()
		}
		declared := slices.Contains(order, detection.Provider)
		switch detection.ProviderState {
		case ProviderUnavailable:
			if providerUsable(context, file, detection.Provider, declared) {
				return false
			}
		case ProviderHTTP:
			if !declared || provider.UsesCommandTransport(file, detection.Provider) {
				return false
			}
		}
	}
	return true
}

func binaryOnPath(context Context, name string) bool {
	lookPath := context.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath(name)
	return err == nil
}

func providerUsable(context Context, file config.File, name string, declared bool) bool {
	if !declared {
		return false
	}
	if provider.UsesCommandTransport(file, name) {
		command := file.Models.Providers[name].Command
		if len(command) == 0 {
			command = []string{name}
		}
		return binaryOnPath(context, command[0])
	}
	env := func(key string) string {
		if key == provider.EnvOrder || context.Env == nil {
			return ""
		}
		return context.Env(key)
	}
	cascade, err := provider.BuildCascade(provider.Settings{
		File: file, Credentials: context.CredentialsPath, Env: env,
	})
	if err != nil {
		return false
	}
	for _, candidate := range cascade.Providers {
		if candidate.Name() != name {
			continue
		}
		if credentialed, ok := candidate.(interface{ HasCredential() bool }); ok {
			return credentialed.HasCredential()
		}
		return true
	}
	return false
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func Run(context Context, registry []Entry, options Options) (Result, error) {
	var result Result
	if options.Out == nil {
		options.Out = io.Discard
	}
	if options.In == nil {
		options.In = strings.NewReader("")
	}
	open := Open(context, registry)
	result.Pending = len(open)
	stamps, err := readStamps(context.StampPath)
	if err != nil {
		return result, err
	}
	reader := bufio.NewReader(options.In)
	for _, entry := range open {
		if !options.ListAll && stamps[entry.ID] == context.Version {
			continue
		}
		result.Offered++
		fmt.Fprintf(options.Out, "capability: %s\n", entry.Proposal.Alert)
		if !options.Interactive {
			if !options.ListAll {
				stamps[entry.ID] = context.Version
			}
			continue
		}
		accepted, err := askYesNo(reader, options.Out, entry.Proposal.Prompt)
		if !options.ListAll {
			stamps[entry.ID] = context.Version
		}
		if err != nil {
			return result, err
		}
		if !accepted {
			continue
		}
		input := ""
		if entry.Proposal.InputPrompt != "" {
			fmt.Fprint(options.Out, entry.Proposal.InputPrompt)
			line, readErr := reader.ReadString('\n')
			input = strings.TrimSpace(line)
			if readErr != nil && input == "" {
				return result, fmt.Errorf("no value was supplied for %s", entry.ID)
			}
			if input == "" {
				return result, fmt.Errorf("the value for %s is empty", entry.ID)
			}
		}
		changes := substituteInput(entry.Proposal.Changes, input)
		outcome, err := agentcfg.Edit("roca", context.ConfigPath, func(text string) (string, error) {
			return config.ApplyText(text, changes)
		}, true)
		if err != nil {
			return result, err
		}
		result.Accepted++
		result.Changes = append(result.Changes, outcome)
		fmt.Fprintf(options.Out, "configuration updated: %s", outcome.Path)
		if outcome.Backup != "" {
			fmt.Fprintf(options.Out, " (backup: %s)", outcome.Backup)
		}
		fmt.Fprintln(options.Out)
	}
	if !options.ListAll && result.Offered > 0 {
		if err := writeStamps(context.StampPath, stamps); err != nil {
			return result, err
		}
	}
	return result, nil
}

func askYesNo(reader *bufio.Reader, out io.Writer, prompt string) (bool, error) {
	fmt.Fprintf(out, "%s [y/N]: ", prompt)
	line, err := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	if err != nil && answer == "" {
		return false, nil
	}
	return answer == "y" || answer == "yes", nil
}

func substituteInput(changes []config.Change, input string) []config.Change {
	resolved := append([]config.Change(nil), changes...)
	for i := range resolved {
		resolved[i].Value = replaceInput(resolved[i].Value, input)
		resolved[i].Fields = append([]config.Field(nil), resolved[i].Fields...)
		for j := range resolved[i].Fields {
			resolved[i].Fields[j].Value = replaceInput(resolved[i].Fields[j].Value, input)
		}
	}
	return resolved
}

func replaceInput(value any, input string) any {
	switch typed := value.(type) {
	case string:
		if typed == inputValue {
			return input
		}
	case []string:
		out := append([]string(nil), typed...)
		for i := range out {
			if out[i] == inputValue {
				out[i] = input
			}
		}
		return out
	}
	return value
}

func readStamps(path string) (map[string]string, error) {
	stamps := map[string]string{}
	if path == "" {
		return stamps, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return stamps, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read capability reconciliation stamps at %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &stamps); err != nil {
		return nil, fmt.Errorf("read capability reconciliation stamps at %s: %w", path, err)
	}
	return stamps, nil
}

func writeStamps(path string, stamps map[string]string) error {
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(stamps)
	if err != nil {
		return err
	}
	if err := securefile.Write(path, append(raw, '\n'), 0o600, 0o700); err != nil {
		return fmt.Errorf("write capability reconciliation stamps at %s: %w", path, err)
	}
	return nil
}
