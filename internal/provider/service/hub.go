package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
	"github.com/thellmwhisperer/la-roca/internal/store"
)

func (s *Service) openHub(ctx context.Context) error {
	ops := databaseForVerb(s.resident, StoreVerb, rocaOpsPluginName)
	corpus := databaseForVerb(s.resident, IngestVerb, rocaCorpusPluginName)
	if ops == nil || corpus == nil {
		return fmt.Errorf("the federation hub requires resident %s and %s databases",
			rocaOpsPluginName, rocaCorpusPluginName)
	}
	hub, err := plugin.OpenHub(ctx, s.resident)
	if err != nil {
		return err
	}
	s.hub = hub
	if err := installHubCompatibility(ctx, hub.DB, ops.Schema, corpus.Schema); err != nil {
		return err
	}
	if _, err := hub.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
		return fmt.Errorf("make the federation hub read-only: %w", err)
	}
	s.hubDB, err = store.Transient(hub.DB, s.opts.DBPath)
	if err != nil {
		return err
	}
	var smoke int
	if err := hub.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories").Scan(&smoke); err != nil {
		return fmt.Errorf("smoke the federation compatibility route: %w", err)
	}
	return nil
}

func installHubCompatibility(ctx context.Context, db *sql.DB, opsSchema, corpusSchema string) error {
	ops, corpus := quoteSchema(opsSchema), quoteSchema(corpusSchema)
	statements := []string{
		`CREATE TEMP VIEW memories AS SELECT id, layer, content, metadata, origin,
			source_agent, source_model, source_surface, source_session, source_sequence,
			project, status, supersedes, created_at
		 FROM ` + ops + `.memory_compatibility WHERE source_database = 'core'`,
		`CREATE TEMP VIEW sessions AS SELECT session_id, source_agent, project, started_at,
			ended_at, duration_minutes, title, metadata, source_surface
		 FROM ` + corpus + `.sessions`,
		`CREATE TEMP VIEW exchanges AS SELECT id, session_id,
			exchange_number, is_after_compaction, human_text, agent_text, human_timestamp,
			agent_timestamp, response_latency_ms, model, provider, tokens_in, tokens_out,
			tokens_reasoning, cost_usd
		 FROM ` + corpus + `.exchanges`,
		`CREATE TEMP VIEW tool_uses AS SELECT id, session_id,
			exchange_number, tool_name, tool_params_summary, had_error, error_message,
			initiative_type
		 FROM ` + corpus + `.tool_uses`,
		`CREATE TEMP VIEW thinking_blocks AS SELECT id, session_id,
			exchange_number, position_in_session, depth, caution_ratio, word_count,
			is_after_compaction, full_text
		 FROM ` + corpus + `.thinking_blocks`,
		`CREATE TEMP VIEW ingest_file_state AS SELECT path, source_kind, source_agent,
			project, fingerprint, last_synced_at, last_error, metadata
		 FROM ` + corpus + `.ingest_file_state`,
		`CREATE TEMP TABLE layers (
			name TEXT PRIMARY KEY, description TEXT NOT NULL, schema_file TEXT NOT NULL,
			access_mode TEXT, ingest_allowed INTEGER, is_coordination INTEGER,
			search_excluded INTEGER, alias_of TEXT, added_by TEXT, deprecated INTEGER,
			lifecycle TEXT, capabilities TEXT, since_version TEXT)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("compose the federation compatibility schema: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO layers
			(name, description, schema_file, access_mode, ingest_allowed, is_coordination,
			 search_excluded, alias_of, added_by, deprecated, lifecycle, capabilities,
			 since_version)
		 SELECT name, description, schema_file, access_mode, ingest_allowed, is_coordination,
			 search_excluded, alias_of, added_by, deprecated, lifecycle, capabilities,
			 since_version FROM `+ops+`.layers`); err != nil {
		return fmt.Errorf("compose the federation layer catalogue: %w", err)
	}
	return nil
}

// Compatibility views forward SQLite's FTS handle, so MATCH and bm25 still run
// in the owning database. Only internal legacy search needs these names.
func (s *Service) ensureHubSearchViews(ctx context.Context) (resultErr error) {
	if s.hub == nil {
		return nil
	}
	s.hubSearchMu.Lock()
	defer s.hubSearchMu.Unlock()
	if s.hubSearchReady {
		return nil
	}
	connection, err := s.hub.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = OFF"); err != nil {
		return err
	}
	defer func() {
		_, err := connection.ExecContext(context.WithoutCancel(ctx), "PRAGMA query_only = ON")
		resultErr = errors.Join(resultErr, err)
	}()
	ops := quoteSchema(databaseForVerb(s.resident, StoreVerb, rocaOpsPluginName).Schema)
	corpus := quoteSchema(databaseForVerb(s.resident, IngestVerb, rocaCorpusPluginName).Schema)
	statements := []string{
		`CREATE TEMP VIEW IF NOT EXISTS memories_fts AS SELECT c.id AS rowid, f.content,
			f.memory_records_fts AS memories_fts, f.rank
		 FROM ` + ops + `.memory_records_fts AS f
		 JOIN ` + ops + `.memory_compatibility AS c ON c.physical_id = f.rowid
		 WHERE c.source_database = 'core'`,
		`CREATE TEMP VIEW IF NOT EXISTS exchanges_fts AS SELECT rowid, human_text, agent_text,
			exchanges_fts, rank FROM ` + corpus + `.exchanges_fts`,
		`CREATE TEMP VIEW IF NOT EXISTS thinking_fts AS SELECT rowid, full_text, thinking_fts, rank
		 FROM ` + corpus + `.thinking_fts`,
		`CREATE TEMP VIEW IF NOT EXISTS sessions_fts AS SELECT rowid, title, project, sessions_fts, rank
		 FROM ` + corpus + `.sessions_fts`,
		`CREATE TEMP VIEW IF NOT EXISTS search_state AS SELECT 'lexical_index' AS key, 'built' AS value`,
	}
	for _, statement := range statements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	s.hubSearchReady = true
	return nil
}
