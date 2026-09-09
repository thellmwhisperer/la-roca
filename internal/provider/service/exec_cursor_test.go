package service

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestExecReaderIndependentIngestAttachments(t *testing.T) {
	options := residentTestOptions(t)
	options.CorpusEnabled, options.PluginsEnabled = true, true
	if _, err := rocacorpus.Ensure(options.PluginDir, filepath.Join(filepath.Dir(options.DBPath), "bin"), "test"); err != nil {
		t.Fatal(err)
	}
	for n := range 9 {
		name := fmt.Sprintf("lab%d", n)
		dir := filepath.Join(options.PluginDir, name)
		fixture := t.TempDir()
		if _, err := rocaops.Ensure(fixture, filepath.Join(fixture, "bin"), "test"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(fixture, rocaops.Name), dir); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(dir, rocaops.DatabaseFilename))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<1001)
		INSERT INTO memories(id, layer, content, origin) SELECT i, 'discovery', 'synthetic attachment', 'agent' FROM n`)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		manifest, err := plugin.ReadManifest(filepath.Join(dir, plugin.PackageFilename))
		if err != nil {
			t.Fatal(err)
		}
		manifest.Name = name
		manifest.Databases[0].Alias = name
		manifest.Databases[0].Attachment = plugin.AttachmentOnDemand
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, plugin.PackageFilename), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	svc := openResident(t, options)
	if len(svc.resident) != 2 {
		t.Fatalf("resident seats: %d", len(svc.resident))
	}
	reader := svc.NewExecReader()
	defer reader.Close()
	var jobs [2][]*ExecCursor
	for n := range 9 {
		cursor, err := reader.OpenExecCursor(t.Context(), fmt.Sprintf("SELECT id FROM lab%d.memories ORDER BY id", n), 1000)
		if err != nil {
			t.Fatalf("open independent source %d: %v", n, err)
		}
		jobs[n%2] = append(jobs[n%2], cursor)
		rows, err := cursor.ReadPage()
		if err != nil || len(rows) != 500 {
			t.Fatalf("first page %d: %d rows: %v", n, len(rows), err)
		}
	}
	for job, cursors := range jobs {
		for _, cursor := range cursors {
			rows, err := cursor.ReadPage()
			if err != nil || len(rows) != 500 || fmt.Sprint(rows[0]["id"]) != "501" {
				t.Fatalf("job %d lost position: %v: %v", job, rows, err)
			}
			cursor.Close()
			cursor.Close() // Owner cleanup is idempotent, including helper EOF.
		}
		pool, err := svc.db.ReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if job == 0 {
			want = len(jobs[1])
		}
		if got := pool.Stats().InUse; got != want {
			t.Fatalf("job %d retained connections: in use=%d, want=%d", job, got, want)
		}
		connection, attached, err := svc.openQueryConnection(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var count int
		err = connection.QueryRowContext(t.Context(), "SELECT count(*) FROM pragma_database_list WHERE name NOT IN ('main','temp')").Scan(&count)
		closeQueryConnection(connection, attached)
		if err != nil || count != 2 || count > plugin.MaxAttached {
			t.Fatalf("job %d left foreign attachments: %d: %v", job, count, err)
		}
	}
}
