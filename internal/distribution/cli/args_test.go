package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestNoArgumentCommandsRejectStrayArguments(t *testing.T) {
	root := rootCommand(&cliEnv{features: config.FeaturesConfig{Cron: true}})
	var commands []*cobra.Command
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		if !strings.Contains(cmd.Use, " ") && !cmd.DisableFlagParsing {
			commands = append(commands, cmd)
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
	for _, cmd := range commands {
		t.Run(cmd.CommandPath(), func(t *testing.T) {
			if cmd.Args == nil || reflect.ValueOf(cmd.Args).Pointer() != reflect.ValueOf(cobra.NoArgs).Pointer() {
				t.Fatal("command must declare Args: cobra.NoArgs")
			}
			err := cmd.ValidateArgs([]string{"stray-argument"})
			if err == nil || !strings.Contains(err.Error(), `"stray-argument"`) {
				t.Fatalf("stray argument was not named: %v", err)
			}
		})
	}
}
