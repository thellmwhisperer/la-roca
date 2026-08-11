package service_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/service"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

func TestStoreWritesOneMemoryAndReturnsItsIdentity(t *testing.T) {
	svc, _ := serviceWithPaths(t)

	result, err := svc.Store(context.Background(), service.StoreRequest{
		Layer:   "discovery",
		Content: "  adoption compares structure, never the text of the DDL  ",
		Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if result.ID == 0 {
		t.Fatal("a stored memory with no identity")
	}
	if result.Skipped {
		t.Error("the first write of a content is not a duplicate")
	}

	var content, origin, layer string
	err = svc.DB().SQL().QueryRow(
		"SELECT layer, content, origin FROM memories WHERE id = ?", result.ID).
		Scan(&layer, &content, &origin)
	if err != nil {
		t.Fatalf("read back the stored memory: %v", err)
	}
	if layer != "discovery" {
		t.Errorf("layer = %q, want discovery", layer)
	}
	// The content is stored trimmed, which is what makes the deduplication
	// below compare the same thing the caller sees.
	if content != "adoption compares structure, never the text of the DDL" {
		t.Errorf("content = %q: it is not stored trimmed", content)
	}
	if origin != "agent" {
		t.Errorf("origin = %q, want the agent default", origin)
	}
}

// The audit of a write says which surface wrote it, because a memory written by
// the plug and one written by the shell are indistinguishable afterwards
// otherwise.
func TestStoreRecordsWhichSurfaceWroteIt(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()

	fromThePlug, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "written through the plug",
		Surface: service.SurfaceMCP,
	})
	if err != nil {
		t.Fatalf("Store from the plug: %v", err)
	}
	fromTheShell, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "written through the shell",
		Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatalf("Store from the shell: %v", err)
	}

	if got := surfaceOf(t, svc, fromThePlug.ID); got != service.SurfaceMCP {
		t.Errorf("surface = %q, want %q", got, service.SurfaceMCP)
	}
	if got := surfaceOf(t, svc, fromTheShell.ID); got != service.SurfaceCLI {
		t.Errorf("surface = %q, want %q", got, service.SurfaceCLI)
	}
}

func TestStoreKeepsTheMetadataTheCallerSentAlongsideTheAudit(t *testing.T) {
	svc, _ := serviceWithPaths(t)

	result, err := svc.Store(context.Background(), service.StoreRequest{
		Layer: "handoff", Content: "a handoff with its own notes",
		Surface:  service.SurfaceCLI,
		Metadata: map[string]any{"session_id": "abc-123", "trigger": "session_end"},
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}

	metadata := metadataOf(t, svc, result.ID)
	if metadata["session_id"] != "abc-123" {
		t.Errorf("session_id = %v: the caller's metadata was lost", metadata["session_id"])
	}
	if metadata["trigger"] != "session_end" {
		t.Errorf("trigger = %v: the caller's metadata was lost", metadata["trigger"])
	}
	if metadata["surface"] != service.SurfaceCLI {
		t.Errorf("surface = %v: the audit was lost", metadata["surface"])
	}
}

// Deduplication in the (layer, status, project) scope keeps a repeated hook from
// writing the same handoff twice.
func TestStoreDeduplicatesTheSameContentInTheSameScope(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()
	request := service.StoreRequest{
		Layer: "discovery", Content: "the same thing twice", Surface: service.SurfaceCLI,
	}

	first, err := svc.Store(ctx, request)
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	second, err := svc.Store(ctx, request)
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}

	if !second.Skipped {
		t.Error("the second write of the same content is not declared a duplicate")
	}
	if second.ID != first.ID {
		t.Errorf("id = %d, want the one already there (%d)", second.ID, first.ID)
	}
	var rows int
	if err := svc.DB().SQL().QueryRow(
		"SELECT COUNT(*) FROM memories WHERE content = 'the same thing twice'").
		Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d rows, want 1: the deduplication did not hold", rows)
	}
}

func TestStoreDoesNotDeduplicateAcrossProjects(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()

	first, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "same text, another project",
		Project: "one", Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatalf("first Store: %v", err)
	}
	second, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "same text, another project",
		Project: "another", Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatalf("second Store: %v", err)
	}
	if second.Skipped || second.ID == first.ID {
		t.Error("two projects sharing a text are two memories, not one")
	}
}

func TestStoreDeduplicatesOnlyAgainstCurrentMemories(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()

	original, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "original", Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "replacement", Supersedes: original.ID,
		Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatal(err)
	}

	currentAgain, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "replacement", Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !currentAgain.Skipped || currentAgain.ID != replacement.ID {
		t.Fatalf("current duplicate = %+v, want skipped id %d", currentAgain, replacement.ID)
	}

	staleAgain, err := svc.Store(ctx, service.StoreRequest{
		Layer: "discovery", Content: "original", Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if staleAgain.Skipped {
		t.Fatalf("superseded content was treated as current: %+v", staleAgain)
	}
}

// The alias travels: a `handover` written by anybody lands in `handoff`, which
// is what the session-lifecycle reader looks for.
func TestStoreNormalizesTheLayerThroughTheRegistryAliases(t *testing.T) {
	svc, _ := serviceWithPaths(t)

	result, err := svc.Store(context.Background(), service.StoreRequest{
		Layer: "handover", Content: "an alias of handoff", Surface: service.SurfaceCLI,
	})
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	var layer string
	if err := svc.DB().SQL().QueryRow(
		"SELECT layer FROM memories WHERE id = ?", result.ID).Scan(&layer); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if layer != "handoff" {
		t.Errorf("layer = %q, want handoff: the alias did not resolve", layer)
	}
}

func TestStoreRefusesWhatTheSchemaWouldRefuseAnyway(t *testing.T) {
	svc, _ := serviceWithPaths(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		request service.StoreRequest
		says    string
	}{
		{"no layer", service.StoreRequest{Content: "something"}, "layer"},
		{"no content", service.StoreRequest{Layer: "discovery", Content: "   "}, "content"},
		{"an origin outside the contract",
			service.StoreRequest{Layer: "discovery", Content: "x", Origin: "robot"}, "origin"},
		{"an empty plugin origin",
			service.StoreRequest{Layer: "discovery", Content: "x", Origin: "plugin:"}, "origin"},
		{"a plugin origin with a path",
			service.StoreRequest{Layer: "discovery", Content: "x", Origin: "plugin:bad/name"}, "origin"},
		{"a status outside the contract",
			service.StoreRequest{Layer: "discovery", Content: "x", Status: "half"}, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Store(ctx, tc.request)
			if err == nil {
				t.Fatal("accepted what the schema would refuse")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error %q does not name %q", err, tc.says)
			}
		})
	}
}

func TestStoreRoundTripsAPluginOrigin(t *testing.T) {
	cases := []struct {
		name string
		open func(*testing.T) *service.Service
	}{
		{"current schema", func(t *testing.T) *service.Service {
			svc, _ := serviceWithPaths(t)
			return svc
		}},
		{"released v1 origin constraint", legacyOriginService},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := tc.open(t)
			result, err := svc.Store(context.Background(), service.StoreRequest{
				Layer: "discovery", Content: "plugin-owned synthetic memory", Origin: "plugin:demo",
				Surface: service.SurfaceCLI,
			})
			if err != nil {
				t.Fatalf("Store: %v", err)
			}
			var origin string
			if err := svc.DB().SQL().QueryRow(
				"SELECT origin FROM memories WHERE id = ?", result.ID).Scan(&origin); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if origin != "plugin:demo" {
				t.Errorf("origin = %q, want plugin:demo", origin)
			}
		})
	}
}

func legacyOriginService(t *testing.T) *service.Service {
	path := filepath.Join(t.TempDir(), "roca.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.SQL().Exec(`CREATE TABLE memories (
		id INTEGER PRIMARY KEY AUTOINCREMENT, layer TEXT NOT NULL, content TEXT NOT NULL,
		metadata TEXT DEFAULT '{}', origin TEXT NOT NULL CHECK (origin IN ('human', 'agent', 'cron')),
		source_agent TEXT, project TEXT, status TEXT DEFAULT 'active', supersedes INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	svc, err := service.Open(service.Options{DBPath: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	return svc
}

func TestStoreRefusesBeforeAnyDatabaseIOWhenReadOnly(t *testing.T) {
	svc := readOnlyService(t)

	_, err := svc.Store(context.Background(), service.StoreRequest{
		Layer: "discovery", Content: "this must not land", Surface: service.SurfaceCLI,
	})
	if err == nil {
		t.Fatal("a read-only installation accepted a write")
	}
	// The refusal belongs to the service; both surfaces render the same message.
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error %q does not name read-only mode", err)
	}
	if !strings.Contains(err.Error(), "store") {
		t.Errorf("error %q does not name the refused operation", err)
	}
}

func surfaceOf(t *testing.T, svc *service.Service, id int64) string {
	t.Helper()
	surface, _ := metadataOf(t, svc, id)["surface"].(string)
	return surface
}

func metadataOf(t *testing.T, svc *service.Service, id int64) map[string]any {
	t.Helper()
	var raw string
	if err := svc.DB().SQL().QueryRow(
		"SELECT metadata FROM memories WHERE id = ?", id).Scan(&raw); err != nil {
		t.Fatalf("read back the metadata: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("the stored metadata is not JSON (%q): %v", raw, err)
	}
	return parsed
}
