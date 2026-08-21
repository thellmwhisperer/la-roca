// Package incrementality provides file fingerprints and primitives for loading
// and recording unchanged-pass state in La Roca's ingest_file_state table.
package incrementality

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
)

// Target describes the part of a scanner target needed for fingerprinting and
// state persistence. Callers keep parsing and discovery details in their own
// types.
type Target struct {
	Path        string
	Kind        string
	SourceAgent string
	Project     string

	// ParserVersion invalidates a previously recorded fingerprint when the
	// target's reader learns to extract more from an otherwise unchanged file.
	ParserVersion string
	// IncludeSQLiteWAL includes the database's write-ahead log. A committed
	// change can live only in the WAL until the owning process checkpoints it.
	IncludeSQLiteWAL bool
	// CompanionPaths are read-only inputs that enrich a SQLite target. When
	// IncludeSQLiteWAL is true, their fingerprints travel with the primary
	// artifact so either source changing reopens the same normalized snapshot.
	CompanionPaths []string
}

// FileState is the persisted unchanged-pass state for one path. Metadata is
// opaque to this package and is returned so callers can recover their own
// per-target summaries without another query.
type FileState struct {
	Fingerprint string
	LastError   string
	Metadata    json.RawMessage
}

// Fingerprint returns a path identity made from metadata and a content digest.
// Size and mtime alone collide after timestamp-preserving restores.
func Fingerprint(path string) (string, error) {
	metadata, err := MetadataFingerprint(path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return metadata + ":" + fmt.Sprintf("%x", digest.Sum(nil)), nil
}

// MetadataFingerprint returns the cheap size-and-mtime prefix used by
// Fingerprint. It can recognize an unchanged regular file when reading its
// content transiently fails, but it must not be used as the normal skip key.
func MetadataFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(info.Size(), 10) + ":" +
		strconv.FormatInt(info.ModTime().UnixNano(), 10), nil
}

// TargetFingerprint fingerprints a target, including its parser version and,
// when requested, its SQLite WAL and companion inputs.
func TargetFingerprint(target Target) (string, error) {
	main, err := Fingerprint(target.Path)
	if err != nil {
		return "", err
	}
	if !target.IncludeSQLiteWAL {
		return parserAwareFingerprint(main, target.ParserVersion), nil
	}
	wal, err := Fingerprint(target.Path + "-wal")
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		wal = "none"
	}
	combined := main + ":wal:" + wal
	if len(target.CompanionPaths) > 0 {
		digest := sha256.New()
		paths := slices.Clone(target.CompanionPaths)
		slices.Sort(paths)
		for _, path := range paths {
			fingerprint, fingerprintErr := Fingerprint(path)
			switch {
			case fingerprintErr == nil:
			case os.IsNotExist(fingerprintErr):
				fingerprint = "missing"
			default:
				fingerprint = "unreadable"
			}
			_, _ = fmt.Fprintf(digest, "%d:%s:%d:%s", len(path), path,
				len(fingerprint), fingerprint)
		}
		combined += ":companions:" + fmt.Sprintf("%x", digest.Sum(nil))
	}
	return parserAwareFingerprint(combined, target.ParserVersion), nil
}

func parserAwareFingerprint(fingerprint, version string) string {
	if version == "" {
		return fingerprint
	}
	return fingerprint + ":parser:" + version
}

// LoadState reads ingest_file_state once and keys the result by target path.
// One query avoids a round trip for every file in a large scanner pass.
func LoadState(ctx context.Context, db *sql.DB) (map[string]FileState, error) {
	rows, err := db.QueryContext(ctx, `SELECT path, COALESCE(fingerprint, ''),
		COALESCE(last_error, ''), COALESCE(metadata, '{}') FROM ingest_file_state`)
	if err != nil {
		return nil, fmt.Errorf("read the ingest state: %w", err)
	}
	defer rows.Close()

	state := map[string]FileState{}
	for rows.Next() {
		var path, fingerprint, failure string
		var metadata []byte
		if err := rows.Scan(&path, &fingerprint, &failure, &metadata); err != nil {
			return nil, fmt.Errorf("read the ingest state: %w", err)
		}
		state[path] = FileState{
			Fingerprint: fingerprint,
			LastError:   failure,
			Metadata:    json.RawMessage(slices.Clone(metadata)),
		}
	}
	return state, rows.Err()
}

// Unchanged reports whether a target can be skipped without being opened. A
// recorded error always forces another read because the failure may be transient
// or fixed in the current build.
func Unchanged(state map[string]FileState, path, fingerprint string) bool {
	known, ok := state[path]
	if !ok || known.LastError != "" || known.Fingerprint == "" {
		return false
	}
	return known.Fingerprint == fingerprint
}

// UnchangedMetadata reports whether metadata matches the prefix of a successful
// content fingerprint. It is intended only as a fallback after content
// fingerprinting fails for a non-database target.
func UnchangedMetadata(state map[string]FileState, path, metadata string) bool {
	known, ok := state[path]
	return ok && known.LastError == "" && metadata != "" &&
		strings.HasPrefix(known.Fingerprint, metadata+":")
}

// RecordState upserts one target's state in the caller's transaction. Keeping
// state and normalized writes in the same transaction prevents a crash from
// recording a fingerprint for data that never committed.
func RecordState(ctx context.Context, tx *sql.Tx, target Target, fingerprint string,
	failure string, summary map[string]any) error {
	payload := "{}"
	if len(summary) > 0 {
		encoded, err := json.Marshal(summary)
		if err != nil {
			return fmt.Errorf("encode state metadata for %s: %w", target.Path, err)
		}
		payload = string(encoded)
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ingest_file_state
		  (path, source_kind, source_agent, project, fingerprint, last_synced_at, last_error, metadata)
		VALUES (?, ?, ?, ?, ?, datetime('now'), ?, ?)
		ON CONFLICT(path) DO UPDATE SET
		  source_kind = excluded.source_kind,
		  source_agent = excluded.source_agent,
		  project = excluded.project,
		  fingerprint = excluded.fingerprint,
		  last_synced_at = datetime('now'),
		  last_error = excluded.last_error,
		  metadata = excluded.metadata`,
		target.Path, target.Kind, nullIfEmpty(target.SourceAgent),
		nullIfEmpty(target.Project), fingerprint, nullIfEmpty(failure), payload)
	if err != nil {
		return fmt.Errorf("record the state of %s: %w", target.Path, err)
	}
	return nil
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
