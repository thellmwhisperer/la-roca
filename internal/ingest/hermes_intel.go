package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/ingest/parsers"
)

const hermesPromptPreview = 240

type hermesIntel struct {
	usage      map[string][]map[string]any
	routing    map[string][]hermesRoutingRow
	routingIDs map[string][]hermesRoutingRow
	prompts    map[string]map[string]any
	exclusions []parsers.Discard
}

type hermesRoutingRow struct {
	index int
	entry map[string]any
}

func hermesSurface(channel string) string {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return "Hermes"
	}
	return "Hermes/" + channel
}

func readHermesIntel(ctx context.Context, db *sql.DB) (hermesIntel, error) {
	intel := hermesIntel{
		usage:      map[string][]map[string]any{},
		routing:    map[string][]hermesRoutingRow{},
		routingIDs: map[string][]hermesRoutingRow{},
		prompts:    map[string]map[string]any{},
	}
	if err := hermesLoadUsage(ctx, db, &intel); err != nil {
		return hermesIntel{}, err
	}
	if err := hermesLoadRouting(ctx, db, &intel); err != nil {
		return hermesIntel{}, err
	}
	if err := hermesLoadPrompts(ctx, db, &intel); err != nil {
		return hermesIntel{}, err
	}
	intel.exclusions = hermesNamedStoreExclusions(ctx, db)
	return intel, nil
}

func attachHermesIntel(session *parsers.Session, source row, intel hermesIntel) {
	if session.Metadata == nil {
		session.Metadata = map[string]any{}
	}
	if channel := source.text("source"); channel != "" {
		session.Metadata["channel"] = channel
	}
	if usage := intel.usage[session.ID]; len(usage) > 0 {
		session.Metadata["model_usage"] = usage
	}
	key := source.text("session_key")
	seenRouting := map[int]bool{}
	routing := make([]map[string]any, 0, len(intel.routing[key])+len(intel.routingIDs[session.ID]))
	for _, sourceRow := range append(intel.routing[key], intel.routingIDs[session.ID]...) {
		if seenRouting[sourceRow.index] {
			continue
		}
		seenRouting[sourceRow.index] = true
		routing = append(routing, sourceRow.entry)
	}
	if len(routing) > 0 {
		session.Metadata["routing"] = routing
	}
	if prompt := intel.prompts[source.text("system_prompt_hash")]; prompt != nil {
		session.Metadata["system_prompt"] = prompt
	}
}

func hermesLoadUsage(ctx context.Context, db *sql.DB, intel *hermesIntel) error {
	if !hermesHasTable(ctx, db, "session_model_usage") {
		return nil
	}
	rows, err := queryRows(ctx, db, `SELECT * FROM session_model_usage`)
	if err != nil {
		return err
	}
	for _, source := range rows {
		id := source.text("session_id")
		if id == "" {
			continue
		}
		intel.usage[id] = append(intel.usage[id], hermesUsageRow(source))
	}
	return nil
}

func hermesUsageRow(source row) map[string]any {
	payload := map[string]any{}
	for key, column := range map[string]string{
		"model":              "model",
		"provider":           "billing_provider",
		"api_base":           "billing_base_url",
		"requests":           "api_call_count",
		"tokens_in":          "input_tokens",
		"tokens_out":         "output_tokens",
		"cache_read_tokens":  "cache_read_tokens",
		"cache_write_tokens": "cache_write_tokens",
		"reasoning_tokens":   "reasoning_tokens",
		"estimated_cost_usd": "estimated_cost_usd",
		"actual_cost_usd":    "actual_cost_usd",
		"pricing_source":     "cost_source",
		"first_seen":         "first_seen",
		"last_seen":          "last_seen",
	} {
		if source.has(column) {
			payload[key] = source[column]
		}
	}
	return payload
}

func hermesLoadRouting(ctx context.Context, db *sql.DB, intel *hermesIntel) error {
	if !hermesHasTable(ctx, db, "gateway_routing") {
		return nil
	}
	rows, err := queryRows(ctx, db, `SELECT * FROM gateway_routing`)
	if err != nil {
		return err
	}
	for index, source := range rows {
		entry := hermesRoutingEntry(source)
		routingRow := hermesRoutingRow{index: index, entry: entry}
		if key := source.text("session_key"); key != "" {
			intel.routing[key] = append(intel.routing[key], routingRow)
		}
		if id, _ := entry["session_id"].(string); id != "" {
			intel.routingIDs[id] = append(intel.routingIDs[id], routingRow)
		}
	}
	return nil
}

func hermesRoutingEntry(source row) map[string]any {
	entry := map[string]any{}
	if raw := source.text("entry_json"); raw != "" {
		var parsed map[string]any
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			entry = parsed
		}
	}
	for _, key := range []string{"scope", "session_key", "updated_at"} {
		if source.has(key) {
			entry[key] = source[key]
		}
	}
	return entry
}

func hermesLoadPrompts(ctx context.Context, db *sql.DB, intel *hermesIntel) error {
	if !hermesHasTable(ctx, db, "system_prompts") {
		return nil
	}
	rows, err := queryRows(ctx, db, `SELECT hash, prompt FROM system_prompts`)
	if err != nil {
		return err
	}
	for _, source := range rows {
		hash := source.text("hash")
		if hash == "" {
			continue
		}
		prompt := source.text("prompt")
		intel.prompts[hash] = map[string]any{
			"hash":    hash,
			"preview": parsers.Clip(prompt, hermesPromptPreview),
			"bytes":   len(prompt),
		}
	}
	return nil
}

func hermesHasTable(ctx context.Context, db *sql.DB, name string) bool {
	columns, err := tableColumns(ctx, db, name)
	return err == nil && len(columns) > 0
}

func hermesNamedStoreExclusions(ctx context.Context, db *sql.DB) []parsers.Discard {
	var discards []parsers.Discard
	for _, table := range []string{
		"messages_fts", "messages_fts_trigram",
	} {
		if count := hermesTableCount(ctx, db, table); count > 0 {
			discards = append(discards, parsers.Discard{
				Reason:   "Hermes FTS shadow tables are not conversation content",
				Category: "Hermes FTS shadow tables are not conversation content",
				ByDesign: true,
			})
			break
		}
	}
	for _, table := range []string{
		"session_turn_leases", "compression_locks", "gateway_hygiene_state", "async_delegations",
	} {
		if hermesTableCount(ctx, db, table) > 0 {
			discards = append(discards, parsers.Discard{
				Reason:   "Hermes locks and leases are not conversation content",
				Category: "Hermes locks and leases are not conversation content",
				ByDesign: true,
			})
			break
		}
	}
	return discards
}

func hermesTableCount(ctx context.Context, db *sql.DB, table string) int {
	if !hermesHasTable(ctx, db, table) {
		return 0
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		return 0
	}
	return count
}
