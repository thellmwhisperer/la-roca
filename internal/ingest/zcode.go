package ingest

import (
	"context"
	"database/sql"
	"strings"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

const zCodeScope = "zcode"

var zCodeSchema = []foreignTable{
	{"schema_migration", []string{"id", "app_version", "time_applied"}},
	{"session", []string{"id", "project_id", "workspace_id", "parent_id", "slug",
		"directory", "path", "title", "version", "task_type", "time_created", "time_updated"}},
	{"message", []string{"id", "session_id", "time_created", "time_updated", "data", "sequence"}},
	{"part", []string{"id", "message_id", "session_id", "time_created", "time_updated", "data", "sequence"}},
}

type zCodeModel struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
	Variant    string `json:"variant"`
}

type zCodeMessage struct {
	Role     string `json:"role"`
	ParentID string `json:"parentID"`
	Time     struct {
		Created   *float64 `json:"created"`
		Completed *float64 `json:"completed"`
	} `json:"time"`
	Model      *zCodeModel           `json:"model"`
	ModelID    string                `json:"modelID"`
	ProviderID string                `json:"providerID"`
	Cost       *float64              `json:"cost"`
	Tokens     *durableMessageTokens `json:"tokens"`
	Synthetic  bool                  `json:"synthetic"`
	Semantics  struct {
		Origin               string `json:"origin"`
		Kind                 string `json:"kind"`
		UIVisibility         string `json:"uiVisibility"`
		TranscriptVisibility string `json:"transcriptVisibility"`
	} `json:"semantics"`
}

type zCodeRow = durableMessageRow[zCodeMessage]

// ReadZCode projects the durable store used by ZCode Desktop 3.10.2 onto corpus
// records. The source stays live while it is read, so every query uses the same
// query-only foreign-database boundary as the other desktop SQLite harnesses.
func ReadZCode(ctx context.Context, path string) (parsers.Records, []string, error) {
	db, err := openForeignSource(ctx, "ZCode", path, zCodeSchema)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	defer db.Close()

	latestMigration, err := zCodeLatestMigration(ctx, db)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	sessions, err := queryRows(ctx, db, `SELECT id, project_id, workspace_id, parent_id,
		slug, directory, path, title, version, task_type, time_created, time_updated
		FROM session ORDER BY time_created, id`)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	messages, malformedMessages, err := readDurableMessageDocuments[zCodeMessage](ctx, db,
		`SELECT id, session_id, time_created, time_updated, data FROM message
		 ORDER BY session_id, sequence, time_created, id`, true)
	if err != nil {
		return parsers.Records{}, nil, err
	}
	parts, malformedParts, err := readDurableMessageDocuments[zCodeMessage](ctx, db,
		`SELECT id, message_id, session_id, time_created, time_updated, data FROM part
		 ORDER BY session_id, message_id, sequence, time_created, id`, false)
	if err != nil {
		return parsers.Records{}, nil, err
	}

	store := prepareDurableMessageStore(messages, parts, malformedMessages, malformedParts)
	records := parsers.Records{
		MessageCoverage: &parsers.MessageCoverage{Seen: len(messages), Skipped: map[string]int{}},
		Seen:            parsers.Seen{Sessions: len(sessions), Messages: len(messages)},
	}
	project := func(source row, messages, parts []zCodeRow) (parsers.Session, int, []parsers.Discard) {
		return zCodeSession(path, latestMigration, source, messages, parts, records.MessageCoverage)
	}
	projected, deferred, discards, complaints :=
		projectDurableSessions("ZCode", sessions, store, records.MessageCoverage, project)
	records.Sessions, records.Deferred, records.Discards = projected, deferred, discards
	return records, complaints, nil
}

func zCodeLatestMigration(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := queryRows(ctx, db, `SELECT id FROM schema_migration
		ORDER BY time_applied DESC, id DESC LIMIT 1`)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	return rows[0].text("id"), nil
}

func zCodeSession(path, latestMigration string, source row, messages, parts []zCodeRow,
	coverage *parsers.MessageCoverage) (parsers.Session, int, []parsers.Discard) {
	native := source.text("id")
	created, _ := source.number("time_created")
	updated, _ := source.number("time_updated")
	session := parsers.Session{
		ID:                     zCodeScope + ":" + native,
		SourceAgent:            zCodeScope,
		Project:                baseName(source.text("directory")),
		StartedAt:              isoFromMS(created),
		EndedAt:                isoFromMS(updated),
		Title:                  source.text("title"),
		SnapshotUpdatedAt:      isoFromMS(updated),
		Snapshot:               true,
		ExchangeKeyScope:       zCodeScope,
		PruneUnmappedExchanges: true,
	}
	if minutes := int((updated - created) / 60000); minutes >= 0 {
		session.DurationMinutes = &minutes
	}
	if parent := source.text("parent_id"); parent != "" {
		session.ParentID = zCodeScope + ":" + parent
	}
	session.Metadata = parsers.WithoutEmpty(map[string]any{
		"source_db":               path,
		"native_session_id":       native,
		"project_id":              source.text("project_id"),
		"workspace_id":            source.text("workspace_id"),
		"parent_id":               source.text("parent_id"),
		"slug":                    source.text("slug"),
		"session_directory":       source.text("directory"),
		"session_path":            source.text("path"),
		"version":                 source.text("version"),
		"task_type":               source.text("task_type"),
		"updated_at":              isoFromMS(updated),
		"latest_schema_migration": latestMigration,
	})
	var sessionDeferred int
	var discards []parsers.Discard
	session.Exchanges, sessionDeferred, discards = zCodeExchanges(messages, parts, coverage)
	discards = append(discards, zCodePartTelemetry(parts)...)
	parsers.PlaceThinking(session.Exchanges)
	return session, sessionDeferred, discards
}

func zCodePartTelemetry(parts []zCodeRow) []parsers.Discard {
	var discards []parsers.Discard
	for _, part := range parts {
		switch part.part.Type {
		case "step-start", "step-finish":
			discards = append(discards, parsers.Excluded("ZCode step telemetry"))
		case "file":
			discards = append(discards, parsers.Excluded("ZCode attachment reference"))
		}
	}
	return discards
}

func zCodeExchanges(messages, parts []zCodeRow,
	coverage *parsers.MessageCoverage) ([]parsers.Exchange, int, []parsers.Discard) {
	partsByMessage := map[string][]zCodeRow{}
	for _, part := range parts {
		partsByMessage[part.messageID] = append(partsByMessage[part.messageID], part)
	}
	var exchanges []parsers.Exchange
	var discards []parsers.Discard
	deferred := 0
	for _, message := range messages {
		messageParts := partsByMessage[message.id]
		role := message.message.Role
		switch {
		case message.message.Semantics.Kind == "timeline_event":
			coverage.Skipped["timeline telemetry"]++
			discards = append(discards, parsers.Excluded("ZCode timeline message"))
			continue
		case message.message.Synthetic || message.message.Semantics.UIVisibility == "hidden" ||
			message.message.Semantics.TranscriptVisibility == "hidden":
			coverage.Skipped["synthetic or hidden message"]++
			discards = append(discards, parsers.Excluded("ZCode synthetic or hidden message"))
			continue
		case role != "user" && role != "assistant":
			coverage.Skipped["unsupported message role: "+role]++
			discards = append(discards, parsers.Excluded("ZCode unsupported message role"))
			continue
		case role == "assistant" && (message.message.Time.Completed == nil || durableMessageHasLiveTool(messageParts)):
			deferred++
			coverage.Skipped["assistant message still being written"]++
			continue
		}

		model, provider := message.message.recordedModel()
		if model == "" {
			coverage.Skipped["message declares no model"]++
			discards = append(discards, parsers.Discard{
				Reason:   "ZCode message " + message.id + " declares no model",
				Category: "ZCode message declares no model",
			})
			continue
		}
		number := len(exchanges) + 1
		exchange := parsers.Exchange{
			Number:      number,
			SourceID:    message.id,
			Fingerprint: zCodeFingerprint(message, messageParts),
			Provenance:  zCodeProvenance(message.message, model, provider),
		}
		populateDurableExchange(&exchange, role, durableTextOfParts(messageParts, "text"),
			zCodeAssistantContent(messageParts), message.message.Time.Created,
			message.message.Time.Completed, message.created, message.updated)
		for _, part := range messageParts {
			switch part.part.Type {
			case "reasoning":
				if part.part.Text != "" {
					exchange.Thinking = append(exchange.Thinking, parsers.Thinking{
						Text: part.part.Text, WordCount: len(strings.Fields(part.part.Text)),
					})
				}
			case "tool":
				status := part.part.status()
				if status != "completed" && status != "error" {
					continue
				}
				params, failure := part.part.toolState()
				exchange.Tools = append(exchange.Tools, parsers.ToolUse{
					Name: part.part.Tool, ParamsSummary: params,
					HadError: status == "error", ErrorMessage: failure,
				})
			}
		}
		exchanges = append(exchanges, exchange)
		coverage.Converted++
	}
	return exchanges, deferred, discards
}

func (m zCodeMessage) recordedModel() (string, string) {
	if m.ModelID != "" {
		return m.ModelID, m.ProviderID
	}
	if m.Model != nil {
		return m.Model.ModelID, m.Model.ProviderID
	}
	return "", ""
}

func zCodeAssistantContent(parts []zCodeRow) string {
	if text := durableTextOfParts(parts, "text"); text != "" {
		return text
	}
	var tools []string
	for _, part := range parts {
		if part.part.Type != "tool" || part.part.Tool == "" {
			continue
		}
		status := part.part.status()
		if status == "completed" || status == "error" {
			tools = append(tools, "[tool "+part.part.Tool+"]")
		}
	}
	return strings.Join(tools, "\n\n")
}

func zCodeProvenance(message zCodeMessage, model, provider string) parsers.Provenance {
	var tally parsers.UsageTally
	addDurableMessageUsage(&tally, message.Cost, message.Tokens)
	return tally.Provenance(model, provider)
}

func zCodeFingerprint(message zCodeRow, parts []zCodeRow) string {
	projection := durablePartProjection(parts, func(part durableMessagePart) string {
		if part.Type == "text" || part.Type == "reasoning" {
			return part.Text
		}
		return ""
	})
	return durableMessageFingerprint(message.id, message.message, projection)
}
