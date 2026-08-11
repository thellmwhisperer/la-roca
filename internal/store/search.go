package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/thellmwhisperer/la-roca/data"
)

// EnsureSearchSchema leaves the search artefacts created: the FTS5 lexical
// index with its triggers and its index-state marker. It is idempotent and
// touches no data: the whole DDL is written with IF NOT EXISTS.
//
// Creating the index does not fill it. That is the indexing's job, which knows
// how to go in parts and can be resumed; here it is only guaranteed that the
// tables exist.
func EnsureSearchSchema(ctx context.Context, db *DB) error {
	return applySchema(ctx, db, data.SearchSchema, "search")
}

// DerivedTables are the ones the search schema creates, including the shadow
// tables FTS5 creates on its own behind each virtual table.
//
// They are computed by applying both schemas to an in-memory database and
// subtracting: a hand-written list would go stale the moment somebody added an
// index, and the price of being wrong is that adoption declares orphan a table
// the product itself creates.
var DerivedTables = sync.OnceValues(func() (map[string]bool, error) {
	ctx := context.Background()
	identityOnly, closeDB, err := referenceDB(ctx)
	if err != nil {
		return nil, err
	}
	identity, err := readStructure(ctx, identityOnly)
	closeDB()
	if err != nil {
		return nil, err
	}

	withSearch, closeDB, err := referenceDB(ctx)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	if _, err := withSearch.ExecContext(ctx, data.SearchSchema); err != nil {
		return nil, fmt.Errorf("apply the search schema to the reference: %w", err)
	}
	all, err := readStructure(ctx, withSearch)
	if err != nil {
		return nil, err
	}

	derived := map[string]bool{}
	for name := range all.tables {
		if _, isIdentity := identity.tables[name]; !isIdentity {
			derived[name] = true
		}
	}
	return derived, nil
})

// isDerived says whether a table is a search artefact. On a failure to compute
// the list the answer is no, which is the safe side: the table is reported as an
// orphan, which is noise, instead of being hidden, which is a lie.
func isDerived(name string) bool {
	derived, err := DerivedTables()
	if err != nil {
		return false
	}
	return derived[name]
}
