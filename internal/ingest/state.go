package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

// Ingest idempotency has two levels because one is not enough.
//
// **File level.** `ingest_file_state` keeps, per path, the source kind, the agent,
// the project, a fingerprint, the last sync and the last error. Before a file is
// read, its current fingerprint is compared with the stored one; when they match
// the file is skipped whole, without being opened. That is what makes a repeated
// `roca ingest` cheap, and the operator's real flow runs it repeatedly.
//
// **Record level.** A fingerprint is not enough for a log that grows: a session
// file changes on every turn and its fingerprint changes whole. The writer
// matches replayed turns within their session before enrichment or insertion and
// leaves conflicting or ambiguous anchors alone. The unique
// `idx_exchanges_session_number` index is the final defence against a duplicate;
// re-reading a grown file inserts only new exchanges.
//
// The debt v1 does not inherit is that this used to be two contracts: the live
// route kept fingerprints and the full reconciliation did not, so the table was
// empty on a machine with many sessions. Here every route is this route.

// Fingerprint is a file's identity for the skip decision: metadata plus a
// content digest. Size and mtime alone collide after timestamp-preserving
// restores, which would otherwise mark changed transcripts as synced forever.
func Fingerprint(path string) (string, error) {
	metadata, err := metadataFingerprint(path)
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

func metadataFingerprint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(info.Size(), 10) + ":" +
		strconv.FormatInt(info.ModTime().UnixNano(), 10), nil
}

// targetFingerprint includes SQLite's write-ahead log for database sources.
// Commits can live only in that sidecar until the owning process checkpoints,
// leaving the main database's size and mtime unchanged.
func targetFingerprint(target Target) (string, error) {
	main, err := Fingerprint(target.Path)
	if err != nil {
		return "", err
	}
	if target.Kind != parsers.KindOpenCodeDB && target.Kind != parsers.KindHermesDB {
		return parserAwareFingerprint(target.Kind, main), nil
	}
	wal, err := Fingerprint(target.Path + "-wal")
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		wal = "none"
	}
	return parserAwareFingerprint(target.Kind, main+":wal:"+wal), nil
}

// parserVersions is the reading each source kind currently gets. The version
// travels inside the watermark, so a build that learned to read more of a source
// re-reads the files it already synced instead of trusting a fingerprint earned
// by a poorer reading. What actually lands is still decided record by record:
// the shared writer matches historical turns before additive enrichment, refuses
// conflicting or ambiguous anchors, and leaves the unique index as the final
// duplicate guard. The provenance backfill only fills columns that are NULL, so
// a plain `roca ingest` enriches a corpus without writing a second copy of it.
//
// A kind absent from here is one whose reading has not changed since the
// watermark was introduced, and its files stay skipped.
var parserVersions = map[parsers.Kind]string{
	parsers.KindClaudeSession:           "claude-session-v6",
	parsers.KindCoworkAudit:             "cowork-audit-v6",
	parsers.KindSubagent:                "subagent-v6",
	parsers.KindCodexSession:            "codex-session-v6",
	parsers.KindPiSession:               "pi-session-v6",
	parsers.KindOpenCodeDB:              "opencode-v6",
	parsers.KindHermesDB:                "hermes-v6",
	parsers.KindClaudeWebConversations:  "claude-web-conversations-v4",
	parsers.KindChatGPTWebConversations: "chatgpt-web-conversations-v2",
}

func parserAwareFingerprint(kind parsers.Kind, fingerprint string) string {
	version, versioned := parserVersions[kind]
	if !versioned {
		return fingerprint
	}
	return fingerprint + ":parser:" + version
}

// FileState is what the database remembers about one path.
type FileState struct {
	Fingerprint string
	LastError   string
}

// LoadState reads the whole state table once. One query beats one query per file:
// a machine with thousands of transcripts would otherwise spend the run on
// round trips.
func LoadState(ctx context.Context, db *sql.DB) (map[string]FileState, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT path, COALESCE(fingerprint, ''), COALESCE(last_error, '') FROM ingest_file_state`)
	if err != nil {
		return nil, fmt.Errorf("read the ingest state: %w", err)
	}
	defer rows.Close()

	state := map[string]FileState{}
	for rows.Next() {
		var path, fingerprint, failure string
		if err := rows.Scan(&path, &fingerprint, &failure); err != nil {
			return nil, fmt.Errorf("read the ingest state: %w", err)
		}
		state[path] = FileState{Fingerprint: fingerprint, LastError: failure}
	}
	return state, rows.Err()
}

// Unchanged decides whether a file can be skipped without being opened.
//
// A path with an error recorded against it is always re-read: the error may have
// been the disk, the agent writing mid-file, or a bug that has since been fixed,
// and none of those is a reason to never look again.
func Unchanged(state map[string]FileState, path, fingerprint string) bool {
	known, ok := state[path]
	if !ok || known.LastError != "" || known.Fingerprint == "" {
		return false
	}
	return known.Fingerprint == fingerprint
}

func unchangedMetadata(state map[string]FileState, path, metadata string) bool {
	known, ok := state[path]
	return ok && known.LastError == "" && metadata != "" &&
		strings.HasPrefix(known.Fingerprint, metadata+":")
}

// RecordState writes one path's state. The upsert by path is what makes
// re-ingesting never duplicate the state either.
func RecordState(ctx context.Context, tx *sql.Tx, target Target, fingerprint string,
	failure string, summary map[string]any) error {
	payload := "{}"
	if len(summary) > 0 {
		if encoded, err := json.Marshal(summary); err == nil {
			payload = string(encoded)
		}
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
		target.Path, string(target.Kind), nullIfEmpty(target.SourceAgent),
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
