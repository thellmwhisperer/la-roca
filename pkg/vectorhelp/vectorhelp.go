// Package vectorhelp owns the next-step hints for `roca vector query`.
// The host resident path and the plugin CLI print the same lines from here.
package vectorhelp

import (
	"fmt"
	"strings"
)

// Hit is the read shape a vector result needs to name a checked SELECT.
type Hit struct {
	Alias       string
	Table       string
	ID          string
	IDColumn    string
	TextColumns []string
}

const narrowWiden = "Run the same query with `--databases <one>` to narrow, or a larger k to widen"

// Query returns the shared help[] for a vector search: a checked SELECT for
// the first hit that declares a read shape, then the narrow-or-widen line.
func Query(hits []Hit) []string {
	var lines []string
	if hint := readHitHint(hits); hint != "" {
		lines = append(lines, hint)
	}
	return append(lines, narrowWiden)
}

func readHitHint(hits []Hit) string {
	for _, hit := range hits {
		if hit.Alias == "" || hit.Table == "" || hit.ID == "" ||
			hit.IDColumn == "" || len(hit.TextColumns) == 0 {
			continue
		}
		return fmt.Sprintf(
			"Run `roca exec \"SELECT %s FROM %s.%s WHERE %s = %s\" --max-chars 2000` to read a hit in full",
			strings.Join(hit.TextColumns, ", "), hit.Alias, hit.Table, hit.IDColumn, sqlLiteral(hit.ID))
	}
	return ""
}

func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
