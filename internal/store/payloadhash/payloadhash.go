// Package payloadhash registers the SQLite function that exact-payload unique
// indexes use so the index stores a digest, not the payload.
package payloadhash

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"time"

	sqlite "modernc.org/sqlite"
)

// SQLFunc is the deterministic scalar the exact-payload unique indexes call.
const SQLFunc = "roca_payload_hash"

var once sync.Once

func init() { Register() }

// Register installs the hash function for every later SQLite connection.
func Register() {
	once.Do(func() {
		if err := sqlite.RegisterDeterministicScalarFunction(SQLFunc, -1, hash); err != nil {
			panic(err)
		}
	})
}

func hash(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	hasher := sha256.New()
	var length [8]byte
	for _, value := range args {
		tag, payload, err := frameValue(value)
		if err != nil {
			return nil, err
		}
		_, _ = hasher.Write([]byte{tag})
		binary.BigEndian.PutUint64(length[:], uint64(len(payload)))
		_, _ = hasher.Write(length[:])
		_, _ = hasher.Write(payload)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func frameValue(value driver.Value) (byte, []byte, error) {
	var encoded [8]byte
	switch value := value.(type) {
	case nil:
		return 0, nil, nil
	case int64:
		binary.BigEndian.PutUint64(encoded[:], uint64(value))
		return 1, encoded[:], nil
	case float64:
		if value == 0 {
			value = 0
		}
		binary.BigEndian.PutUint64(encoded[:], math.Float64bits(value))
		return 2, encoded[:], nil
	case bool:
		if value {
			return 3, []byte{1}, nil
		}
		return 3, []byte{0}, nil
	case string:
		return 4, []byte(value), nil
	case []byte:
		return 5, value, nil
	case time.Time:
		return 6, []byte(value.Format(time.RFC3339Nano)), nil
	default:
		return 0, nil, fmt.Errorf("unsupported SQLite payload type %T", value)
	}
}
