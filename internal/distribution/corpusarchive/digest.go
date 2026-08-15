package corpusarchive

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
)

func SnapshotDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open frozen corpus source %q: %w", path, err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("digest frozen corpus source %q: %w", path, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func canonicalDigest(family string, values ...any) string {
	digest := sha256.New()
	writeCanonical(digest, family)
	for _, value := range values {
		writeCanonical(digest, value)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeCanonical(writer hash.Hash, value any) {
	switch typed := value.(type) {
	case nil:
		_, _ = writer.Write([]byte{0})
	case string:
		_, _ = writer.Write([]byte{1})
		writeBytes(writer, []byte(typed))
	case int64:
		_, _ = writer.Write([]byte{2})
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(typed))
		_, _ = writer.Write(encoded[:])
	case float64:
		_, _ = writer.Write([]byte{3})
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], math.Float64bits(typed))
		_, _ = writer.Write(encoded[:])
	case sql.NullString:
		if typed.Valid {
			writeCanonical(writer, typed.String)
		} else {
			writeCanonical(writer, nil)
		}
	case sql.NullInt64:
		if typed.Valid {
			writeCanonical(writer, typed.Int64)
		} else {
			writeCanonical(writer, nil)
		}
	case sql.NullFloat64:
		if typed.Valid {
			writeCanonical(writer, typed.Float64)
		} else {
			writeCanonical(writer, nil)
		}
	default:
		panic(fmt.Sprintf("unsupported canonical value %T", value))
	}
}

func writeBytes(writer hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}
