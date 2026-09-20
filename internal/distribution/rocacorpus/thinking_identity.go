package rocacorpus

import (
	"context"
	"fmt"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

const thinkingIdentityIndex = "idx_thinking_blocks_identity"

// prepareThinkingIdentity collapses thinking rows that share session, exchange,
// and text before the unique identity index is created. Position is not part of
// that key: an open session that grows used to rewrite it and insert copies.
func prepareThinkingIdentity(ctx context.Context, path string) error {
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		return err
	}
	defer db.Close()
	present, err := tableExistsDB(ctx, db, "thinking_blocks")
	if err != nil || !present {
		return err
	}
	indexed, err := indexExistsDB(ctx, db, thinkingIdentityIndex)
	if err != nil {
		return fmt.Errorf("inspect thinking identity index: %w", err)
	}
	if indexed {
		return nil
	}
	if _, err := db.ExecContext(ctx, `
		DELETE FROM thinking_blocks
		WHERE id NOT IN (
		  SELECT MIN(id) FROM thinking_blocks
		  GROUP BY session_id, exchange_number, full_text
		)`); err != nil {
		return fmt.Errorf("collapse thinking identity copies: %w", err)
	}
	fts, err := tableExistsDB(ctx, db, "thinking_fts")
	if err != nil {
		return err
	}
	if fts {
		if _, err := db.ExecContext(ctx, `INSERT INTO thinking_fts(thinking_fts) VALUES ('rebuild')`); err != nil {
			return fmt.Errorf("rebuild thinking_fts after identity collapse: %w", err)
		}
	}
	return nil
}
