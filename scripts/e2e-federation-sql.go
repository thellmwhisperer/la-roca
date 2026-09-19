//go:build ignore

// Apply frozen seed SQL to a synthetic corpus database using the same
// payload-hash function the product registers. Maintainer freeze only.
package main

import (
	"database/sql"
	"log"
	"os"

	_ "github.com/thellmwhisperer/la-roca/internal/store/payloadhash"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: e2e-federation-sql <database> <sql-file-or-->")
	}
	raw, err := os.ReadFile(os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	db, err := sql.Open("sqlite", os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(string(raw)); err != nil {
		log.Fatal(err)
	}
}
