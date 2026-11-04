//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cucumber/godog"
)

// The hybrid leg of `roca query` finding `roca-vector` where `roca vector`
// finds it. The companion is a shell stub with one fixed hit, so these
// scenarios never load the embedding model.

const stubVectorHit = `{"results":[{"rank":1,"score":0.9,"database":"corpus","table":"exchanges",` +
	`"id":"1","text":"where did we leave the harbor lantern"}],"vector_executed":true}`

func registerVectorCompanionSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^a home initialized with an agent session that mentions "([^"]*)"$`, m.homeWithSessionMentioning)
	ctx.Given(`^vector is switched on under features$`, m.vectorSwitchedOn)
	ctx.Given(`^a stub roca-vector that answers query with that exchange is in ~/.local/bin only$`,
		m.stubVectorInLocalBin)
	ctx.Given(`^PATH does not contain ~/.local/bin$`, m.pathLacksLocalBin)
	ctx.Given(`^no roca-vector exists in ~/.local/bin or on PATH$`, m.noVectorCompanion)
	ctx.Given(`^a stub roca-vector that answers status is in ~/.local/bin$`, m.stubVectorStatusInLocalBin)
	ctx.Given(`^serving = "cutover" is written under layout in config.toml$`, m.servingCutoverByHand)
	ctx.Given(`^config.toml is not valid TOML$`, m.configNotTOML)
	ctx.Given(`^the vector registry declares the ops tables "([^"]*)"$`, m.registryDeclaresOpsTables)
	ctx.Then(`^the vector registry declares the ops tables "([^"]*)"$`, m.registryDeclaresOpsTables)
	ctx.When(`^the operator runs roca query "([^"]*)" as json$`, func(question string) error {
		_, err := m.runWith("roca query "+question+" --json", []string{"query", question, "--json"})
		return err
	})
	ctx.Then(`^the query engines include "([^"]*)"$`, func(engine string) error {
		return m.queryList("engines", func(values []string) bool { return slices.Contains(values, engine) })
	})
	ctx.Then(`^the query engines are only "([^"]*)"$`, func(engine string) error {
		return m.queryList("engines", func(values []string) bool { return slices.Equal(values, []string{engine}) })
	})
	ctx.Then(`^the query notices include "([^"]*)"$`, func(notice string) error {
		return m.queryList("notices", func(values []string) bool { return slices.Contains(values, notice) })
	})
	ctx.Then(`^the query notices do not include "([^"]*)"$`, func(notice string) error {
		return m.queryList("notices", func(values []string) bool {
			return !slices.ContainsFunc(values, func(value string) bool { return strings.Contains(value, notice) })
		})
	})
}

// homeWithSessionMentioning is `roca init --db-path ~/.roca/roca.db` over one
// Claude session whose exchange carries the words.
func (m *world) homeWithSessionMentioning(words string) error {
	cwd := filepath.Join(m.home, "workspace", "harbor")
	session := filepath.Join(m.home, ".claude", "projects", encodeAgentPath(cwd), seededSessionID+".jsonl")
	if err := writeFixture(session, fmt.Sprintf(
		"{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:00Z\",\"cwd\":%q,\"message\":{\"content\":\"where did we leave the %s\"}}\n"+
			"{\"type\":\"assistant\",\"timestamp\":\"2026-08-01T10:00:02Z\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"on the pier\"}]}}\n",
		cwd, words)); err != nil {
		return err
	}
	if err := m.mustRun(m.initCommand(true)); err != nil {
		return err
	}
	return m.noVectorCompanion()
}

func (m *world) vectorSwitchedOn() error {
	path := filepath.Join(m.home, ".roca", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(raw)
	if !strings.Contains(text, "vector = false") {
		return fmt.Errorf("init left no `vector = false` to switch on:\n%s", text)
	}
	return os.WriteFile(path, []byte(strings.Replace(text, "vector = false", "vector = true", 1)), 0o600)
}

func (m *world) stubVectorInLocalBin() error {
	script := "#!/bin/sh\n[ -n \"$ROCA_VECTOR_ROCA_BINARY\" ] && [ -x \"$ROCA_VECTOR_ROCA_BINARY\" ] || exit 1\n" +
		"case \" $* \" in\n*\" query \"*) printf '%s\\n' '" + stubVectorHit + "' ;;\n*) exit 1 ;;\nesac\n"
	path := filepath.Join(m.home, ".local", "bin", "roca-vector")
	if err := writeFixture(path, script); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func (m *world) stubVectorStatusInLocalBin() error {
	script := "#!/bin/sh\ncase \" $* \" in\n*\" status \"*) echo 'stub roca-vector status' ;;\n*) exit 1 ;;\nesac\n"
	path := filepath.Join(m.home, ".local", "bin", "roca-vector")
	if err := writeFixture(path, script); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

// servingCutoverByHand edits config.toml the way an operator does, without
// any roca command that would regenerate the vector registry.
func (m *world) servingCutoverByHand() error {
	path := filepath.Join(m.home, ".roca", "config.toml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, "\n[layout]\nserving = \"cutover\"\n"...), 0o600)
}

func (m *world) configNotTOML() error {
	return os.WriteFile(filepath.Join(m.home, ".roca", "config.toml"), []byte("[features\nvector = \n"), 0o600)
}

// registryDeclaresOpsTables reads the vector registry, the generated JSON
// contract roca-vector consumes, and compares the ops sidecar's tables.
func (m *world) registryDeclaresOpsTables(want string) error {
	raw, err := os.ReadFile(filepath.Join(m.home, ".roca", "plugins", "vector-registry.json"))
	if err != nil {
		return err
	}
	var registry struct {
		Databases []struct {
			Plugin string `json:"plugin"`
			Tables []struct {
				Name string `json:"name"`
			} `json:"tables"`
		} `json:"databases"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		return fmt.Errorf("the vector registry is not JSON: %w", err)
	}
	for _, database := range registry.Databases {
		if database.Plugin != "roca-ops" {
			continue
		}
		var names []string
		for _, table := range database.Tables {
			names = append(names, table.Name)
		}
		slices.Sort(names)
		if got := strings.Join(names, " "); got != want {
			return fmt.Errorf("ops vector tables = %q, want %q", got, want)
		}
		return nil
	}
	return fmt.Errorf("the vector registry declares no ops sidecar:\n%s", raw)
}

func (m *world) pathLacksLocalBin() error {
	localBin := filepath.Join(m.home, ".local", "bin")
	for _, entry := range m.environment() {
		if path, ok := strings.CutPrefix(entry, "PATH="); ok && slices.Contains(filepath.SplitList(path), localBin) {
			return fmt.Errorf("PATH holds %s: %s", localBin, path)
		}
	}
	return nil
}

// noVectorCompanion proves there is nothing to find: ~/.local/bin and every
// directory on the scenario's PATH lack roca-vector.
func (m *world) noVectorCompanion() error {
	directories := []string{filepath.Join(m.home, ".local", "bin")}
	for _, entry := range m.environment() {
		if path, ok := strings.CutPrefix(entry, "PATH="); ok {
			directories = append(directories, filepath.SplitList(path)...)
		}
	}
	for _, directory := range directories {
		if _, err := os.Stat(filepath.Join(directory, "roca-vector")); err == nil {
			return fmt.Errorf("roca-vector exists in %s", directory)
		}
	}
	return nil
}

func (m *world) queryList(key string, holds func([]string) bool) error {
	document, err := m.json()
	if err != nil {
		return err
	}
	raw, _ := document[key].([]any)
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		values = append(values, fmt.Sprint(value))
	}
	if !holds(values) {
		return fmt.Errorf("%s = %q", key, values)
	}
	return nil
}
