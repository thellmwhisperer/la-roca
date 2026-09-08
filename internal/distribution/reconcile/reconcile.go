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
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/agentcfg"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

const (
	ProposalClaudeCLI       = "claude-cli-provider"
	ProposalRetiredProvider = "retired-provider"

	inputValue = "{operator-input}"
)

type ProviderCondition string

const (
	ProviderUnavailable ProviderCondition = "unavailable"
)

// Detection is an AND of observable environment and configuration facts.
// Empty fields impose no condition.
type Detection struct {
	Capability       string
	Binary           string
	Provider         string
	ProviderState    ProviderCondition
	RetiredProvider  bool
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
	ID              string
	Detection       Detection
	Proposal        Proposal
	RetiredProvider string
}

type Context struct {
	Version                string
	ConfigPath             string
	StampPath              string
	LookPath               func(string) (string, error)
	Env                    func(string) string
	File                   config.File
	Capabilities           map[string]bool
	RetiredCredentialPaths map[string]string
	RecoveryBackupPaths    []string
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
func Registry() []Entry { return nil }

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
	if detection.DefaultListEmpty != "" && len(file.DefaultList(detection.DefaultListEmpty)) > 0 {
		return false
	}

	return true
}

func binaryOnPath(context Context, name string) bool {
	return binaryExists(context.LookPath, name)
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
		if entry.RetiredProvider != "" {
			if err := RedactRecoveryBackups(context.RecoveryBackupPaths); err != nil {
				return result, err
			}
		}
		changes := substituteInput(entry.Proposal.Changes, input)
		var outcome agentcfg.Outcome
		if len(changes) > 0 {
			outcome, err = agentcfg.EditWithBackup("roca", context.ConfigPath, func(text string) (string, error) {
				return config.ApplyText(text, changes)
			}, config.RedactProviderSecrets, true)
			if err != nil {
				return result, err
			}
		}
		if entry.RetiredProvider != "" {
			if err := RemoveRetiredCredential(context.RetiredCredentialPaths[entry.RetiredProvider]); err != nil {
				return result, err
			}
		}
		result.Accepted++
		if len(changes) > 0 {
			result.Changes = append(result.Changes, outcome)
			fmt.Fprintf(options.Out, "configuration updated: %s", outcome.Path)
			if outcome.Backup != "" {
				fmt.Fprintf(options.Out, " (backup: %s)", outcome.Backup)
			}
			fmt.Fprintln(options.Out)
		} else if entry.RetiredProvider != "" {
			fmt.Fprintf(options.Out, "retired credential removed: %s\n",
				context.RetiredCredentialPaths[entry.RetiredProvider])
		}
	}
	if !options.ListAll && result.Offered > 0 {
		if err := writeStamps(context.StampPath, stamps); err != nil {
			return result, err
		}
	}
	return result, nil
}

func RedactRecoveryBackups(paths []string) error {
	for _, path := range paths {
		if err := agentcfg.Rewrite(path, config.RedactProviderSecrets); err != nil {
			return fmt.Errorf("redact provider secrets from recovery backup %s: %w", path, err)
		}
	}
	return nil
}

func RemoveRetiredCredential(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete retired provider credential %s: %w", path, err)
	}
	if directory := filepath.Dir(path); filepath.Base(directory) == "credentials" {
		_ = os.Remove(directory)
	}
	return nil
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

func binaryExists(lookup func(string) (string, error), name string) bool {
	if lookup == nil {
		lookup = exec.LookPath
	}
	_, err := lookup(name)
	return err == nil
}
