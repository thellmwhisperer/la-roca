//go:build ignore

// Compact and verify normalized release fixtures with canonical SQLite functions,
// without applying any schema or custody migration from the current branch.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

func main() {
	for _, path := range os.Args[1:] {
		db, err := bundledplugin.OpenDatabase(path, false)
		if err != nil {
			log.Fatal(err)
		}
		if _, err := db.Exec("VACUUM"); err != nil {
			log.Fatal(err)
		}
		var result string
		err = db.QueryRow("PRAGMA integrity_check").Scan(&result)
		closeErr := db.Close()
		if err != nil || result != "ok" || closeErr != nil {
			log.Fatalf("fixture integrity: %s: %q, query=%v, close=%v", path, result, err, closeErr)
		}
		fmt.Println("fixture integrity: ok")
	}
}
