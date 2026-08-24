package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestRootMenuShowsExactlyThePublicCommands(t *testing.T) {
	want := []string{
		"doctor", "explore", "hooks", "ingest", "init", "layers", "model", "playground", "plugin", "plugins", "query", "remote", "store", "tool-call-observer", "uninstall", "update",
	}
	root := rootCommand(&cliEnv{})
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	var got []string
	for _, command := range root.Commands() {
		if !command.Hidden {
			got = append(got, command.Name())
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("visible commands = %v, want %v", got, want)
	}

	var output strings.Builder
	root.SetOut(&output)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("roca --help: %v", err)
	}
	for _, name := range want {
		if !strings.Contains(output.String(), "  "+name+" ") {
			t.Errorf("help does not give %q its own command line:\n%s", name, output.String())
		}
	}
	for _, name := range []string{"completion", "help"} {
		if strings.Contains(output.String(), "\n  "+name+"        ") {
			t.Errorf("help exposes %q:\n%s", name, output.String())
		}
	}

	help := rootCommand(&cliEnv{})
	help.SetOut(&strings.Builder{})
	help.SetArgs([]string{"help", "query"})
	if err := help.Execute(); err != nil {
		t.Fatalf("the hidden standalone help command no longer executes: %v", err)
	}
}

func TestCronAppearsInTheMenuOnlyWhenEnabled(t *testing.T) {
	env := &cliEnv{features: config.FeaturesConfig{Cron: true}, featuresLoaded: true}
	root := rootCommand(env)
	command, _, err := root.Find([]string{"cron"})
	if err != nil || command.Name() != "cron" || command.Hidden {
		t.Fatalf("enabled cron command = %v, err %v", command, err)
	}
}

func TestHiddenCommandsStillHaveHelp(t *testing.T) {
	for _, name := range []string{
		"exec", "health", "index", "login", "mcp", "models", "schema", "skill", "version",
	} {
		t.Run(name, func(t *testing.T) {
			root := rootCommand(&cliEnv{})
			root.SetOut(&strings.Builder{})
			root.SetArgs([]string{name, "--help"})
			if err := root.Execute(); err != nil {
				t.Fatalf("roca %s --help: %v", name, err)
			}
			command, _, err := root.Find([]string{name})
			if err != nil || command.Name() != name || !command.Hidden {
				t.Errorf("hidden command %q is not live: command=%v err=%v", name, command, err)
			}
		})
	}
}

func TestServeLivesUnderMCP(t *testing.T) {
	root := rootCommand(&cliEnv{})
	command, args, err := root.Find([]string{"mcp", "serve"})
	if err != nil || command.Name() != "serve" || len(args) != 0 {
		t.Fatalf("roca mcp serve is not declared: command=%v args=%v err=%v", command, args, err)
	}
	if slices.Contains(commandNames(root.Commands()), "serve") {
		t.Fatal("serve is still a root command")
	}
}

func TestSchemaRejectsUnknownArguments(t *testing.T) {
	root := rootCommand(&cliEnv{})
	root.SetOut(&strings.Builder{})
	root.SetErr(&strings.Builder{})
	root.SetArgs([]string{"schema", "archive-orphans"})
	if err := root.Execute(); err == nil {
		t.Fatal("unknown schema argument exited successfully")
	}
}

func commandNames(commands []*cobra.Command) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name())
	}
	return names
}
