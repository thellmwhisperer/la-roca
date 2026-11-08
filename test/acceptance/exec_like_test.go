//go:build acceptance

package acceptance

import (
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

func registerExecLikeSteps(ctx *godog.ScenarioContext, m *world) {
	ctx.Given(`^the corpus has orchard exchanges$`, func() error {
		db, err := sql.Open("sqlite", filepath.Join(m.home, ".roca", "plugins", "roca-corpus", "roca-corpus.db"))
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.Exec(`INSERT INTO exchanges(id, human_text, agent_text) VALUES
   (91001, 'orchard apples', 'first reply'),
   (91002, 'visit the orchard', 'second reply'),
   (91003, 'unrelated meadow', 'orchard in the reply only');`)
		return err
	})
	ctx.Given(`^the corpus has FTS suggestion edge cases$`, func() error {
		db, err := sql.Open("sqlite", filepath.Join(m.home, ".roca", "plugins", "roca-corpus", "roca-corpus.db"))
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.Exec(`INSERT INTO thinking_blocks(id, full_text) VALUES
   (92001, 'synthetic orchard thinking'), (92002, 'synthetic meadow thinking');
INSERT INTO exchanges(id, human_text, agent_text) VALUES
   (92011, 'synthetic OR marker', 'first reply'),
   (92012, 'synthetic AND marker', 'second reply'),
   (92013, 'synthetic NOT marker', 'third reply'),
   (92014, 'synthetic meadow marker', 'OR AND NOT');`)
		return err
	})
	ctx.When(`^I run the printed roca exec line verbatim$`, func() error {
		for _, line := range strings.Split(m.last.stdout+m.last.stderr, "\n") {
			if strings.HasPrefix(line, `roca exec "`) {
				// Run the actual printed shell command, resolving roca to this fixture's binary.
				command := exec.Command("/bin/sh", "-c", `roca() { "$0" "$@"; }; `+line, m.binaryPath())
				return m.record("printed MATCH command", command)
			}
		}
		return fmt.Errorf("no complete roca exec line in output: %s", m.last.stderr)
	})
	ctx.Then(`^the result contains exactly the orchard exchange IDs$`, func() error {
		if m.last.code != 0 {
			return fmt.Errorf("printed command failed: %s", m.last.stderr)
		}
		if !strings.Contains(m.last.stdout, "rows[2]{id") || !strings.Contains(m.last.stdout, "91001") || !strings.Contains(m.last.stdout, "91002") || strings.Contains(m.last.stdout, "91003") {
			return fmt.Errorf("unexpected orchard IDs: %s", m.last.stdout)
		}
		return nil
	})
	ctx.When(`^I exec the SQL "([^"]*)" with timeout-ms (\d+)$`, func(statement, timeout string) error {
		_, err := m.runWith("roca exec with timeout", []string{"exec", statement, "--timeout-ms", timeout})
		return err
	})
}
