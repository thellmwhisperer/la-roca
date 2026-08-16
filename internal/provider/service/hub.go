package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
	if err := installHubCompatibility(ctx, hub.DB, s, ops.Schema, corpus.Schema); err != nil {
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

func installHubCompatibility(ctx context.Context, db *sql.DB, s *Service,
	opsSchema, corpusSchema string) error {
	ops, corpus := quoteSchema(opsSchema), quoteSchema(corpusSchema)
	statements := []string{
		`CREATE TEMP VIEW memories AS SELECT id, layer, content, metadata, origin,
			source_agent, source_model, source_surface, source_session, source_sequence,
			project, status, supersedes, created_at
		 FROM ` + ops + `.memory_compatibility WHERE source_database = 'core'`,
		`CREATE TEMP VIEW sessions AS SELECT session_id, source_agent, project, started_at,
			ended_at, duration_minutes, title, metadata
		 FROM ` + corpus + `.session_version_memberships WHERE source_database = 'core'`,
		`CREATE TEMP VIEW exchanges AS SELECT source_row_id AS id, session_id,
			exchange_number, is_after_compaction, human_text, agent_text, human_timestamp,
			agent_timestamp, response_latency_ms, model, provider, tokens_in, tokens_out,
			tokens_reasoning, cost_usd
		 FROM ` + corpus + `.exchange_version_memberships WHERE source_database = 'core'`,
		`CREATE TEMP VIEW tool_uses AS SELECT source_row_id AS id, session_id,
			exchange_number, tool_name, tool_params_summary, had_error, error_message,
			initiative_type
		 FROM ` + corpus + `.tool_use_version_memberships WHERE source_database = 'core'`,
		`CREATE TEMP VIEW thinking_blocks AS SELECT source_row_id AS id, session_id,
			exchange_number, position_in_session, depth, caution_ratio, word_count,
			is_after_compaction, full_text
		 FROM ` + corpus + `.thinking_block_version_memberships WHERE source_database = 'core'`,
		`CREATE TEMP VIEW ingest_file_state AS SELECT path, source_kind, source_agent,
			project, fingerprint, last_synced_at, last_error, metadata
		 FROM ` + corpus + `.ingest_file_state_version_memberships WHERE source_database = 'core'`,
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
	for _, layer := range s.registry.Layers {
		if _, err := db.ExecContext(ctx, `INSERT INTO layers
			(name, description, schema_file, access_mode, ingest_allowed, is_coordination,
			 search_excluded, alias_of, added_by, deprecated, lifecycle, capabilities,
			 since_version) VALUES (?, ?, '', 'read-write', ?, ?, ?, ?, ?, ?, ?, '{}', ?)`,
			layer.Name, layer.Description, layer.IngestAllowed, layer.IsCoordination,
			layer.SearchExcluded, orNull(layer.AliasOf), layer.AddedBy, layer.Deprecated,
			layer.Lifecycle, layer.SinceVersion); err != nil {
			return fmt.Errorf("compose the federation layer catalogue: %w", err)
		}
	}
	return nil
}

func (s *Service) ensureHubSearch(ctx context.Context) error {
	if s.hub == nil {
		return nil
	}
	s.hubSearchMu.Lock()
	defer s.hubSearchMu.Unlock()
	if s.hubSearchReady || s.hubSearchFailure != nil {
		return s.hubSearchFailure
	}
	err := s.buildHubSearch(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.hubSearchFailure = err
		}
		return err
	}
	s.hubSearchReady = true
	return nil
}

func (s *Service) buildHubSearch(ctx context.Context) (resultErr error) {
	connection, err := s.hub.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "PRAGMA query_only = OFF"); err != nil {
		return err
	}
	defer func() {
		if _, err := connection.ExecContext(context.WithoutCancel(ctx), "PRAGMA query_only = ON"); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("restore the federation hub read fence: %w", err))
		}
	}()

	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	statements := []string{
		`CREATE VIRTUAL TABLE temp.memories_fts USING fts5(content,
				 content='memories', content_rowid='id', tokenize='unicode61 remove_diacritics 2')`,
		`CREATE VIRTUAL TABLE temp.exchanges_fts USING fts5(human_text, agent_text,
			 content='exchanges', content_rowid='id', tokenize='unicode61 remove_diacritics 2')`,
		`CREATE VIRTUAL TABLE temp.thinking_fts USING fts5(full_text,
			 content='thinking_blocks', content_rowid='id', tokenize='unicode61 remove_diacritics 2')`,
		`CREATE TEMP TABLE hub_session_fts_content
			 (rowid INTEGER PRIMARY KEY, title TEXT, project TEXT)`,
		`INSERT INTO hub_session_fts_content(rowid, title, project)
			 SELECT source_row_id, title, project FROM ` + quoteSchema(s.corpusSchema()) +
			`.session_version_memberships WHERE source_database = 'core'`,
		`CREATE VIRTUAL TABLE temp.sessions_fts USING fts5(title, project,
			 content='hub_session_fts_content', content_rowid='rowid',
			 tokenize='unicode61 remove_diacritics 2')`,
		`INSERT INTO memories_fts(memories_fts) VALUES ('rebuild')`,
		`INSERT INTO exchanges_fts(exchanges_fts) VALUES ('rebuild')`,
		`INSERT INTO thinking_fts(thinking_fts) VALUES ('rebuild')`,
		`INSERT INTO sessions_fts(sessions_fts) VALUES ('rebuild')`,
		`CREATE TEMP TABLE search_state
			 (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT)`,
		`INSERT INTO search_state(key, value) VALUES
				 ('lexical_index', 'built'), ('lexical_tokenizer', 'unicode61-remove-diacritics-2')`,
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("build the in-memory compatibility index: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("publish the in-memory compatibility index: %w", err)
	}
	return nil
}

func (s *Service) corpusSchema() string {
	if database := databaseForVerb(s.resident, IngestVerb, rocaCorpusPluginName); database != nil {
		return database.Schema
	}
	return "plugin_roca_corpus"
}

func needsHubSearch(statement string) bool {
	lower := strings.ToLower(statement)
	for _, table := range []string{"memories_fts", "exchanges_fts", "thinking_fts", "sessions_fts"} {
		if strings.Contains(lower, table) {
			return true
		}
	}
	return false
}
