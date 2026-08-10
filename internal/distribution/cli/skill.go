package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/thellmwhisperer/la-roca/internal/distribution/skill"
)

// skillCommand installs the canonical agent skill that teaches runtimes how to
// use La Roca. Hidden plumbing: bare lists destinations; install writes one
// file per runtime and narrates every path.
func skillCommand(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Install the agent skill that teaches runtimes how to use La Roca",
		Long: "One embedded SKILL.md, copied into each runtime's personal skills\n" +
			"directory. Nothing else is edited.\n\n" +
			"Supported runtimes: " + strings.Join(skill.Runtimes(), ", "),
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return env.listSkillDestinations()
		},
	}
	cmd.AddCommand(skillInstallCommand(env))
	return cmd
}

func skillInstallCommand(env *cliEnv) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "install [runtime]",
		Short: "Write the roca skill into one runtime, or every supported one",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return fmt.Errorf("name one runtime (%s) or ask for --all",
					strings.Join(skill.Runtimes(), ", "))
			}
			runtimes := args
			if all {
				runtimes = skill.Runtimes()
			}
			outcomes := make([]skill.Outcome, 0, len(runtimes))
			for _, runtime := range runtimes {
				path, err := skillFileOf(runtime)
				if err != nil {
					return err
				}
				outcome, err := skill.Install(runtime, path)
				if err != nil {
					return err
				}
				outcomes = append(outcomes, outcome)
			}
			if env.json {
				return env.printJSON(map[string]any{"runtimes": outcomes})
			}
			for _, o := range outcomes {
				verb := "unchanged"
				if o.Changed {
					verb = "wrote"
				}
				env.print("%s: %s %s", o.Runtime, verb, o.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "install into every supported runtime")
	return cmd
}

func (env *cliEnv) listSkillDestinations() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("I do not know where your HOME is")
	}
	type row struct {
		Runtime string `json:"runtime"`
		Path    string `json:"path"`
	}
	rows := make([]row, 0, len(skill.Runtimes()))
	for _, runtime := range skill.Runtimes() {
		path, err := skill.Path(runtime, home, os.Getenv)
		if err != nil {
			return err
		}
		rows = append(rows, row{Runtime: runtime, Path: path})
	}
	if env.json {
		return env.printJSON(map[string]any{"runtimes": rows})
	}
	toonRows := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		toonRows = append(toonRows, map[string]any{"runtime": r.Runtime, "path": r.Path})
	}
	env.print("%s", rowOutput([]string{"runtime", "path"}, toonRows))
	env.print("%s", renderHelp(
		"Run `roca skill install <runtime>` to install one destination",
		"Run `roca skill install --all` to install every destination"))
	return nil
}

func skillFileOf(runtime string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("I do not know where your HOME is")
	}
	return skill.Path(runtime, home, os.Getenv)
}
