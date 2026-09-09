package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

func TestExecReaderIndependentIngestAttachments(t *testing.T) {
	for _, layout := range []ReadLayout{LayoutLegacyServing, LayoutCutover} {
		t.Run(string(layout), func(t *testing.T) {
			testExecReaderIndependentIngestAttachments(t, layout)
		})
	}
}

func testExecReaderIndependentIngestAttachments(t *testing.T, layout ReadLayout) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	options := residentTestOptions(t)
	options.ReadLayout = layout
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
		cursor, err := reader.OpenExecCursor(ctx, fmt.Sprintf("SELECT id FROM lab%d.memories ORDER BY id", n), 1000)
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
		if got := len(reader.cursors); got != want {
			t.Fatalf("job %d retained cursors: %d, want=%d", job, got, want)
		}
		if layout == LayoutCutover {
			want = 0
		}
		if got := pool.Stats().InUse; got != want {
			t.Fatalf("job %d retained connections: in use=%d, want=%d", job, got, want)
		}
		connection, attached, err := svc.openQueryConnection(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		err = connection.QueryRowContext(ctx, "SELECT count(*) FROM pragma_database_list WHERE name NOT IN ('main','temp')").Scan(&count)
		closeQueryConnection(connection, attached)
		if err != nil || count != 2 || count > plugin.MaxAttached {
			t.Fatalf("job %d left foreign attachments: %d: %v", job, count, err)
		}
	}
}

func TestExecReaderCutoverConnections(t *testing.T) {
	fixture := newHubFixture(t)
	seedHubCoreMemory(t, fixture.plugins, 42, "Synthetic cursor marker")
	svc := openHubService(t, fixture, LayoutCutover, nil)
	reader := svc.NewExecReader()
	defer reader.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cursor, err := reader.open(ctx, "SELECT id, content FROM memories", 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := cursor.ReadPage()
	if err != nil || len(rows) != 1 || fmt.Sprint(rows[0]["id"]) != "42" {
		t.Fatalf("compatibility cursor: %v: %v", rows, err)
	}
	connection, attached, release, err := reader.openConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeQueryConnection(connection, attached)
		release()
	}()
	if _, err := connection.ExecContext(ctx, "CREATE TABLE forbidden_write(id INTEGER)"); err == nil {
		t.Fatal("cursor connection accepted a write")
	}
	if _, err := reader.OpenExecCursor(ctx, "DELETE FROM memories", 1000); err == nil {
		t.Fatal("cursor bypassed the SELECT gate")
	}
	reader.Close()
	if len(reader.cursors) != 0 {
		t.Fatal("reader close retained cursors")
	}
	if _, err := os.Stat(fixture.corePath); !os.IsNotExist(err) {
		t.Fatalf("cutover cursor touched roca.db: %v", err)
	}
}
