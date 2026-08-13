package service

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// The two surfaces that write. They travel in the memory row's canonical
// authorship columns, beside the harness and model that made the write.
const (
	SurfaceCLI = "cli"
	SurfaceMCP = "mcp"
	// UnknownAuthor is an explicit absence of evidence. New writes use it
	// instead of NULL; historical NULLs remain untouched and mean the same thing.
	UnknownAuthor = "unknown"
)

var reservedAuthorshipMetadata = []string{"agent", "model", "surface"}

// Authorship is the one identity card stamped on every memory write.
type Authorship struct {
	Agent   string `json:"agent"`
	Model   string `json:"model"`
	Surface string `json:"surface"`
}

func (a Authorship) normalized() Authorship {
	return Authorship{
		Agent: valueOr(a.Agent, UnknownAuthor), Model: valueOr(a.Model, UnknownAuthor),
		Surface: valueOr(a.Surface, UnknownAuthor),
	}
}

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
	// Origin is who creates it: human, agent, cron or plugin:<name>. Empty means
	// agent, which is what an MCP call is.
	Origin     string
	Authorship Authorship
	Project    string
	// Status is active, pending or resolved. Empty means active.
	Status     string
	Supersedes int64
	Metadata   map[string]any
}

// StoreResult is the identity of the memory that is now there, whether this
// call created it or found it already written.
type StoreResult struct {
	ID    int64  `json:"id"`
	Layer string `json:"layer"`
	// Skipped says the content was already stored in this scope. It is not an
	// error: retrying the same write must not create a duplicate memory.
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
	if !validOrigin(origin) {
		return StoreResult{}, fmt.Errorf("origin %q is not one of %s or plugin:<name>",
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
	metadata, err := encodeMetadata(req.Metadata)
	if err != nil {
		return StoreResult{}, err
	}
	var expiresAt any
	if s.opts.RocaOpsEnabled {
		expiresAt, err = explicitExpiry(req.Metadata)
		if err != nil {
			return StoreResult{}, err
		}
	}
	if _, err := s.ensureSchema(ctx); err != nil {
		return StoreResult{}, err
	}

	result := StoreResult{
		Layer:     physical,
		Version:   s.opts.Version,
		SourceSHA: s.opts.Commit,
	}
	authorship := req.Authorship
	authorship = authorship.normalized()
	target := s.db
	if s.opts.RocaOpsEnabled {
		target = s.ops
		if existing, found, err := identicalMemory(ctx, s.db.SQL(), physical, status, content, req.Project); err != nil {
			return StoreResult{}, err
		} else if found {
			result.ID, result.Skipped = existing, true
			return result, nil
		}
	}
	err = target.Write(ctx, func(tx *sql.Tx) error {
		if existing, found, err := identicalMemory(ctx, tx, physical, status, content, req.Project); err != nil {
			return err
		} else if found {
			result.ID, result.Skipped = existing, true
			return nil
		}

		// Released v1 databases carry the original three-value CHECK. The service
		// is now the authoritative validator for the expanded shape, so allow this
		// one validated insert without making an existing database inoperable.
		pluginOrigin := strings.HasPrefix(origin, "plugin:")
		if pluginOrigin {
			if _, err := tx.ExecContext(ctx, "PRAGMA ignore_check_constraints = ON"); err != nil {
				return fmt.Errorf("open the plugin origin compatibility gate: %w", err)
			}
		}
		statement := `INSERT INTO memories (layer, content, metadata, origin, source_agent,
			                       source_model, source_surface, project, status, supersedes)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
		arguments := []any{physical, content, metadata, origin, authorship.Agent, authorship.Model,
			authorship.Surface, orNull(req.Project), status, orNull(req.Supersedes)}
		if s.opts.RocaOpsEnabled {
			statement = `INSERT INTO memories (layer, content, metadata, origin, source_agent,
			                       source_model, source_surface, project, status, supersedes, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			arguments = append(arguments, expiresAt)
		}
		outcome, err := tx.ExecContext(ctx, statement, arguments...)
		if pluginOrigin {
			if _, closeErr := tx.ExecContext(context.WithoutCancel(ctx),
				"PRAGMA ignore_check_constraints = OFF"); closeErr != nil {
				return fmt.Errorf("close the plugin origin compatibility gate: %w", closeErr)
			}
		}
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

func explicitExpiry(metadata map[string]any) (any, error) {
	value, present := metadata["expires_at"]
	if !present || value == nil {
		return nil, nil
	}
	written, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("metadata expires_at must be an RFC3339 string")
	}
	parsed, err := time.Parse(time.RFC3339, written)
	if err != nil {
		return nil, fmt.Errorf("metadata expires_at must be an RFC3339 string: %w", err)
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

type memoryQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func identicalMemory(ctx context.Context, db memoryQuerier, layer, status, content,
	project string) (int64, bool, error) {
	var existing int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM memories
		 WHERE id NOT IN (SELECT supersedes FROM memories WHERE supersedes IS NOT NULL)
		   AND layer = ? AND status = ? AND content = ?
		   AND (project = ? OR (project IS NULL AND ? IS NULL))
		 LIMIT 1`,
		layer, status, content, orNull(project), orNull(project)).Scan(&existing)
	switch {
	case err == nil:
		return existing, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	default:
		return 0, false, fmt.Errorf("look for an identical memory: %w", err)
	}
}

type RocaOpsDrainResult struct {
	Before  string `json:"before"`
	Removed int64  `json:"removed"`
}

// DrainRocaOps removes only rows carrying an explicit expiry at or before the
// operator-supplied instant. Nothing calls it automatically.
func (s *Service) DrainRocaOps(ctx context.Context, before time.Time) (RocaOpsDrainResult, error) {
	if s.opts.ReadOnly {
		return RocaOpsDrainResult{}, refuseReadOnly("drain roca-ops")
	}
	if !s.opts.RocaOpsEnabled || s.ops == nil {
		return RocaOpsDrainResult{}, fmt.Errorf("features.roca_ops is disabled")
	}
	if before.IsZero() {
		return RocaOpsDrainResult{}, fmt.Errorf("a drain cutoff is required")
	}
	cutoff := before.UTC().Format(time.RFC3339Nano)
	result := RocaOpsDrainResult{Before: cutoff}
	err := s.ops.Write(ctx, func(tx *sql.Tx) error {
		outcome, err := tx.ExecContext(ctx, `DELETE FROM memories
			WHERE expires_at IS NOT NULL AND julianday(expires_at) <= julianday(?)`, cutoff)
		if err != nil {
			return fmt.Errorf("drain expired roca-ops memories: %w", err)
		}
		result.Removed, err = outcome.RowsAffected()
		return err
	})
	return result, err
}

func validOrigin(origin string) bool {
	if slices.Contains(validOrigins, origin) {
		return true
	}
	name, ok := strings.CutPrefix(origin, "plugin:")
	if !ok || name == "" {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.", r) {
			return false
		}
	}
	return true
}

// encodeMetadata keeps caller tags away from the canonical authorship columns.
// A reserved key is refused rather than dropped: a write that silently loses a
// tag the caller sent is a write whose result does not say what was stored.
func encodeMetadata(metadata map[string]any) (string, error) {
	for _, key := range reservedAuthorshipMetadata {
		if _, reserved := metadata[key]; !reserved {
			continue
		}
		return "", fmt.Errorf(
			"metadata key %q is reserved: a memory's identity is system stamped into its own "+
				"authorship columns, never taken from the metadata. Name the writer with "+
				"`roca store --agent <harness> --model <model>` and store the rest under "+
				"another key", key)
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
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
