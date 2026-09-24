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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin thinking identity collapse: %w", err)
	}
	defer tx.Rollback()
	remaps, err := tableExists(tx, "thinking_block_id_remaps")
	if err != nil {
		return err
	}
	if remaps {
		if _, err := tx.ExecContext(ctx, `
			WITH survivors AS (
			  SELECT id, MAX(id) OVER (
			    PARTITION BY session_id, exchange_number, full_text
			  ) AS canonical_id FROM thinking_blocks
			)
			UPDATE thinking_block_id_remaps
			SET canonical_id = (
			  SELECT canonical_id FROM survivors
			  WHERE id = thinking_block_id_remaps.canonical_id
			)
			WHERE canonical_id IN (SELECT id FROM survivors WHERE id <> canonical_id)`); err != nil {
			return fmt.Errorf("redirect thinking identity aliases: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM thinking_blocks
		WHERE id NOT IN (
		  SELECT MAX(id) FROM thinking_blocks
		  GROUP BY session_id, exchange_number, full_text
		)`); err != nil {
		return fmt.Errorf("collapse thinking identity copies: %w", err)
	}
	fts, err := tableExists(tx, "thinking_fts")
	if err != nil {
		return err
	}
	if fts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO thinking_fts(thinking_fts) VALUES ('rebuild')`); err != nil {
			return fmt.Errorf("rebuild thinking_fts after identity collapse: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit thinking identity collapse: %w", err)
	}
	return nil
}
