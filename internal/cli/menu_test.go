/*
@overview Contracts the deliberately small root help menu and the live hidden CLI surface. ~100 lines, no public symbols.

	READING GUIDE
	-------------
	1. Start at TestRootMenuShowsExactlyThePublicNine
	2. Read TestHiddenCommandsStillHaveHelp
	3. Finish at TestServeLivesUnderMCP

	MAIN FLOW
	---------
	rootCommand -> Cobra discovery/help -> exact public and hidden surface assertions

	PUBLIC API
	----------
	None; this file tests package-private command assembly.

	INTERNALS
	---------
	TestRootMenuShowsExactlyThePublicNine, TestHiddenCommandsStillHaveHelp,
	TestServeLivesUnderMCP, commandNames

@exports
@deps slices/strings/testing, Cobra command assembly
*/
package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// -- 1/4 CORE · TestRootMenuShowsExactlyThePublicNine -- <- START HERE

func TestRootMenuShowsExactlyThePublicNine(t *testing.T) {
	want := []string{
		"doctor", "ingest", "init", "login", "query", "store", "uninstall", "update",
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

// -/ 1/4

// -- 2/4 HELPER · TestHiddenCommandsStillHaveHelp --

func TestHiddenCommandsStillHaveHelp(t *testing.T) {
	for _, name := range []string{
		"bench", "exec", "health", "hook", "index", "logout", "mcp", "models", "schema", "skill", "version",
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

// -/ 2/4

// -- 3/4 HELPER · TestServeLivesUnderMCP --

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

// -/ 3/4

// -- 4/4 HELPER · commandNames --

func commandNames(commands []*cobra.Command) []string {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, command.Name())
	}
	return names
}

// -/ 4/4
