// @overview Regression for storage-law upgrades before named migration ledgers.
// READING GUIDE: start at TestApplySchemaBeforeNamedMigrationLedger.
// MAIN FLOW: legacy corpus -> ApplySchema twice -> preserved harvest and digests.
// @exports TestApplySchemaBeforeNamedMigrationLedger
// @deps rocacorpus.ApplySchema; diet_test.go fixture helpers.
package rocacorpus_test

import (
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/rocacorpus"
)

// -- 1/1 CORE · TestApplySchemaBeforeNamedMigrationLedger -- <- START HERE
func TestApplySchemaBeforeNamedMigrationLedger(t *testing.T) {
	db, path := openCorpusDB(t)
	seedFatCorpus(t, db)
	// Older bundled homes have archive tables but no named migration ledger.
	if _, err := db.Exec(`DROP TABLE plugin_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rocacorpus.ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db = reapplySchemaAndReopen(t, path)
	defer db.Close()
	assertCountQuery(t, db, `SELECT COUNT(*) FROM exchanges
		WHERE human_text = 'diet prompt' AND agent_text = 'diet answer'`, 1)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM exchange_versions
		WHERE session_id = 'sess-1' AND length(version_digest) = 64`, 1)
	assertCountQuery(t, db, `SELECT COUNT(*) FROM plugin_migrations
		WHERE migration = 'corpus-archive-reconciliation-v1' AND migration_state = 'verified'`, 0)
}

// -/ 1/1
