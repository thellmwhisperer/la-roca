package ingest

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

type sqliteSerializer interface {
	Serialize() ([]byte, error)
}

// ReadCursor freezes one live Cursor database into memory before parsing it.
// Cursor may be writing through WAL while ingest runs; the serialized image is
// one SQLite read transaction and never points the parser at the live files.
func ReadCursor(ctx context.Context, path string) (parsers.Records, []string, error) {
	return readCursorDatabase(ctx, path, parsers.FileMeta{
		Path: path, FileName: filepath.Base(path), SourceAgent: "cursor",
	})
}

// ReadCursorStore is the store.db-era reader. Sidecar bytes are the sibling
// meta.json (title, cwd, timestamps) when the scan found one.
func ReadCursorStore(ctx context.Context, path string, meta parsers.FileMeta) (parsers.Records, []string, error) {
	if meta.FileName == "" {
		meta.FileName = filepath.Base(path)
	}
	if meta.Path == "" {
		meta.Path = path
	}
	if meta.SourceAgent == "" {
		meta.SourceAgent = "cursor"
	}
	return readCursorDatabase(ctx, path, meta)
}

func readCursorDatabase(ctx context.Context, path string, meta parsers.FileMeta) (parsers.Records, []string, error) {
	kind := parsers.KindCursorDB
	if meta.FileName == "store.db" {
		kind = parsers.KindCursorStore
	}
	registered, ok := parsers.Lookup(string(kind))
	if !ok {
		return parsers.Records{}, nil, fmt.Errorf("Cursor database parser is not registered")
	}
	header, err := readSQLiteHeader(path)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	file := parsers.File{Content: header, Meta: meta}
	if !cursorSQLiteSnapshotName(meta.FileName) ||
		len(header) < 16 || string(header[:16]) != "SQLite format 3\x00" {
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

func cursorSQLiteSnapshotName(name string) bool {
	return name == "state.vscdb" || name == "ai-code-tracking.db" || name == "store.db"
}

// readSQLiteHeader reads only the leading 16 bytes a SQLite file opens with.
// The magic is enough to route a candidate before the whole database is frozen
// into a snapshot, so a foreign file is never buffered in full.
func readSQLiteHeader(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	header := make([]byte, 16)
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return header[:n], nil
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
