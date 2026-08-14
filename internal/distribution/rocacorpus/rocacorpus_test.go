package rocacorpus_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	_ "modernc.org/sqlite"
)

func TestTheBundledCorpusOwnsThePerennialHarvestSchema(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	installed, err := rocacorpus.Ensure(root, t.TempDir(), "v-test")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Risk != plugininstall.DataOnly || installed.Executable != "" {
		t.Fatalf("bundle risk = %s, executable = %q", installed.Risk, installed.Executable)
	}

	descriptor, err := plugin.Inspect(installed.Name, installed.Directory)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := plugin.Validate(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Semantic.Attachment != plugin.AttachmentResident ||
		!descriptor.Semantic.Custody || len(validated.Tables) != 9 {
		t.Fatalf("corpus contract = %+v, visible tables = %d",
			descriptor.Semantic, len(validated.Tables))
	}

	db, err := sql.Open("sqlite", descriptor.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`INSERT INTO sessions (session_id, title) VALUES ('fixture-session', 'cobalt atlas')`,
		`INSERT INTO memories (layer, content, origin) VALUES ('project', 'preserved cobalt atlas', 'cron')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO memories (layer, content, origin, provenance)
		VALUES ('project', 'operational record', 'agent', 'agent')`); err == nil {
		t.Fatal("corpus accepted an operational provenance")
	}

	checks := []struct {
		name      string
		statement string
		want      string
	}{
		{"first-class provenance", "SELECT provenance FROM memories", "harvest-file"},
		{"memory FTS", `SELECT COUNT(*) FROM memories_fts WHERE memories_fts MATCH 'cobalt'`, "1"},
		{"session FTS", `SELECT COUNT(*) FROM sessions_fts WHERE sessions_fts MATCH 'atlas'`, "1"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			var got string
			if err := db.QueryRow(check.statement).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != check.want {
				t.Fatalf("result = %q, want %q", got, check.want)
			}
		})
	}
}
