package service

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The two surfaces that write. It travels into the memory's own metadata
// because v1 has no audit table and adding one would change the identity schema
// every adoption compares: the row that was written is where the record of who
// wrote it belongs.
const (
	SurfaceCLI = "cli"
	SurfaceMCP = "mcp"
)

// surfaceKey is the metadata key carrying the audit. It is reserved: a caller's
// own metadata never overwrites it.
const surfaceKey = "surface"

// What the schema's CHECK constraints admit. They are validated here, before
// any database I/O, so both surfaces get the same message instead of a SQLite
// constraint error rendered two different ways.
var (
	validOrigins  = []string{"human", "agent", "cron"}
	validStatuses = []string{"active", "pending", "resolved"}
)

// StoreRequest is one memory to write.
type StoreRequest struct {
	Layer   string
	Content string
	// Origin is who creates it: human, agent or cron. Empty means agent, which
	// is what a plug call and a hook both are.
	Origin      string
	SourceAgent string
	Project     string
	// Status is active, pending or resolved. Empty means active.
	Status     string
	Supersedes int64
	Metadata   map[string]any
	// Surface is which of the two surfaces is writing, for the audit.
	Surface string
}

// StoreResult is the identity of the memory that is now there, whether this
// call created it or found it already written.
type StoreResult struct {
	ID    int64  `json:"id"`
	Layer string `json:"layer"`
	// Skipped says the content was already stored in this scope. It is not an
	// error: a hook that fires twice over the same session must not write the
	// same handoff twice.
	Skipped   bool   `json:"skipped,omitempty"`
	Version   string `json:"version"`
	SourceSHA string `json:"source_sha"`
}

// Store writes one memory. It is the write half of the product, and the same
// object the plug's `roca_store` and the shell's `roca store` both call.
//
// Deduplication compares content, layer, status and project among active
// memories.
func (s *Service) Store(ctx context.Context, req StoreRequest) (StoreResult, error) {
	if s.opts.ReadOnly {
		return StoreResult{}, refuseReadOnly("store")
	}
	layer := strings.TrimSpace(req.Layer)
	if layer == "" {
		return StoreResult{}, fmt.Errorf("a layer is required to store a memory")
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return StoreResult{}, fmt.Errorf("the content of the memory is required")
	}
	origin := valueOr(req.Origin, "agent")
	if !slices.Contains(validOrigins, origin) {
		return StoreResult{}, fmt.Errorf("origin %q is not one of %s",
			origin, strings.Join(validOrigins, ", "))
	}
	status := valueOr(req.Status, "active")
	if !slices.Contains(validStatuses, status) {
		return StoreResult{}, fmt.Errorf("status %q is not one of %s",
			status, strings.Join(validStatuses, ", "))
	}

	// An alias written by anybody lands in the physical layer the readers look
	// for: a `handover` is a `handoff` the moment it is stored, never at the
	// moment somebody queries it.
	physical := s.registry.Resolve(layer, layer)
	metadata, err := encodeMetadata(req.Metadata, req.Surface)
	if err != nil {
		return StoreResult{}, err
	}

	result := StoreResult{
		Layer:     physical,
		Version:   s.opts.Version,
		SourceSHA: s.opts.Commit,
	}
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var existing int64
		row := tx.QueryRowContext(ctx,
			`SELECT id FROM memories
			 WHERE supersedes IS NULL AND layer = ? AND status = ? AND content = ?
			   AND (project = ? OR (project IS NULL AND ? IS NULL))
			 LIMIT 1`,
			physical, status, content, orNull(req.Project), orNull(req.Project))
		switch err := row.Scan(&existing); {
		case err == nil:
			result.ID, result.Skipped = existing, true
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("look for an identical memory: %w", err)
		}

		outcome, err := tx.ExecContext(ctx,
			`INSERT INTO memories (layer, content, metadata, origin, source_agent,
			                       project, status, supersedes)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			physical, content, metadata, origin, orNull(req.SourceAgent),
			orNull(req.Project), status, orNull(req.Supersedes))
		if err != nil {
			return fmt.Errorf("store the memory: %w", err)
		}
		result.ID, err = outcome.LastInsertId()
		return err
	})
	if err != nil {
		return StoreResult{}, err
	}
	return result, nil
}

// encodeMetadata merges the caller's metadata with the audit of which surface
// wrote it. The audit wins: it is not the caller's to declare.
func encodeMetadata(metadata map[string]any, surface string) (string, error) {
	merged := make(map[string]any, len(metadata)+1)
	maps.Copy(merged, metadata)
	if surface != "" {
		merged[surfaceKey] = surface
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("the metadata is not serializable: %w", err)
	}
	return string(encoded), nil
}

func valueOr(value, fallback string) string {
	return cmp.Or(strings.TrimSpace(value), fallback)
}

// orNull is the SQL NULL a zero value stands for. An empty layer, an absent
// project and a memory that supersedes nothing are all "there is no value here",
// and writing "" or 0 instead would make the column's own IS NULL lie.
func orNull[T comparable](value T) any {
	var zero T
	if value == zero {
		return nil
	}
	return value
}
