// Package payloadhash registers the SQLite function that exact-payload unique
// indexes use so the index stores a digest, not the payload.
package payloadhash

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"sync"

	sqlite "modernc.org/sqlite"
)

// SQLFunc is the deterministic scalar the exact-payload unique indexes call.
const SQLFunc = "roca_payload_hash"

var once sync.Once

func init() { Register() }

// Register installs the hash function for every later SQLite connection.
func Register() {
	once.Do(func() {
		if err := sqlite.RegisterDeterministicScalarFunction(SQLFunc, 1, hash); err != nil {
			panic(err)
		}
	})
}

func hash(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	var payload []byte
	switch value := args[0].(type) {
	case nil:
	case string:
		payload = []byte(value)
	case []byte:
		payload = value
	default:
		payload = []byte(fmt.Sprint(value))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
