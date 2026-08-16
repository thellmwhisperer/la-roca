package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

type sqliteSerializer interface {
	Serialize() ([]byte, error)
}

// ReadCursor freezes one live Cursor database into memory before parsing it.
// Cursor may be writing through WAL while ingest runs; the serialized image is
// one SQLite read transaction and never points the parser at the live files.
func ReadCursor(ctx context.Context, path string) (parsers.Records, []string, error) {
	registered, ok := parsers.Lookup(string(parsers.KindCursorDB))
	if !ok {
		return parsers.Records{}, nil, fmt.Errorf("Cursor database parser is not registered")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	meta := parsers.FileMeta{Path: path, FileName: filepath.Base(path), SourceAgent: "cursor"}
	file := parsers.File{Content: raw, Meta: meta}
	if (meta.FileName != "state.vscdb" && meta.FileName != "ai-code-tracking.db") ||
		len(raw) < 16 || string(raw[:16]) != "SQLite format 3\x00" {
		records, err := registered.Parse(file)
		return records, nil, err
	}
	snapshot, err := serializeForeign(ctx, path)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	records, err := registered.Parse(parsers.File{Content: snapshot, Meta: meta})
	return records, nil, err
}

func serializeForeign(ctx context.Context, path string) ([]byte, error) {
	db, err := openForeign(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		return nil, fmt.Errorf("begin immutable snapshot of %q: %w", path, err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var schemaVersion int
	if err := conn.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		return nil, fmt.Errorf("establish immutable snapshot of %q: %w", path, err)
	}
	var snapshot []byte
	err = conn.Raw(func(driverConn any) error {
		serializer, ok := driverConn.(sqliteSerializer)
		if !ok {
			return fmt.Errorf("SQLite driver cannot serialize a snapshot")
		}
		var serializeErr error
		snapshot, serializeErr = serializer.Serialize()
		return serializeErr
	})
	if err != nil {
		return nil, fmt.Errorf("serialize immutable snapshot of %q: %w", path, err)
	}
	return snapshot, nil
}
