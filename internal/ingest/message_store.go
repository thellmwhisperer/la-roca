package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

const (
	durableMessageParamsBudget = 500
	durableMessageErrorBudget  = 1000
)

type durableMessageTokens struct {
	Input     *float64 `json:"input"`
	Output    *float64 `json:"output"`
	Reasoning *float64 `json:"reasoning"`
	Cache     *struct {
		Read  *float64 `json:"read"`
		Write *float64 `json:"write"`
	} `json:"cache"`
}

// Keep the established OpenCode test and reader vocabulary while sharing the
// identical durable token document with ZCode.
type openCodeTokens = durableMessageTokens

type durableMessagePart struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Tool  string          `json:"tool"`
	Hash  string          `json:"hash"`
	Files []string        `json:"files"`
	State json.RawMessage `json:"state"`
}

// OpenCode named this shape first. ZCode 3.10.2 persists the same part document.
type openCodePart = durableMessagePart

func (p durableMessagePart) status() string {
	if len(p.State) == 0 {
		return ""
	}
	var object struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(p.State, &object); err == nil && object.Status != "" {
		return object.Status
	}
	var text string
	if err := json.Unmarshal(p.State, &text); err == nil {
		return text
	}
	return ""
}

func (p durableMessagePart) toolState() (input, failure string) {
	var state struct {
		Input json.RawMessage `json:"input"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(p.State, &state) != nil {
		return "", ""
	}
	if len(state.Input) > 0 && string(state.Input) != "null" {
		var compact bytes.Buffer
		if json.Compact(&compact, state.Input) == nil {
			input = compact.String()
		}
	}
	if len(state.Error) > 0 && string(state.Error) != "null" {
		if json.Unmarshal(state.Error, &failure) != nil {
			var compact bytes.Buffer
			if json.Compact(&compact, state.Error) == nil {
				failure = compact.String()
			}
		}
	}
	return parsers.Clip(input, durableMessageParamsBudget),
		parsers.Clip(failure, durableMessageErrorBudget)
}

type durableMessageRow[M any] struct {
	id        string
	sessionID string
	messageID string
	created   *float64
	updated   *float64
	message   M
	part      durableMessagePart
}

func readDurableMessageDocuments[M any](ctx context.Context, db *sql.DB,
	statement string, isMessage bool) ([]durableMessageRow[M], map[string]string, error) {
	rows, err := queryRows(ctx, db, statement)
	if err != nil {
		return nil, nil, err
	}
	malformed := map[string]string{}
	out := make([]durableMessageRow[M], 0, len(rows))
	for _, record := range rows {
		item := durableMessageRow[M]{
			id: record.text("id"), sessionID: record.text("session_id"),
			messageID: record.text("message_id"),
		}
		if value, ok := record.number("time_created"); ok {
			item.created = &value
		}
		if value, ok := record.number("time_updated"); ok {
			item.updated = &value
		}
		document := any(&item.part)
		if isMessage {
			document = &item.message
		}
		if err := json.Unmarshal([]byte(record.text("data")), document); err != nil {
			malformed[item.sessionID] = "malformed_json"
		}
		out = append(out, item)
	}
	return out, malformed, nil
}

func durableRowsBySession[M any](rows []durableMessageRow[M]) map[string][]durableMessageRow[M] {
	grouped := map[string][]durableMessageRow[M]{}
	for _, item := range rows {
		grouped[item.sessionID] = append(grouped[item.sessionID], item)
	}
	return grouped
}

type durableMessageStore[M any] struct {
	malformed         map[string]string
	messagesBySession map[string][]durableMessageRow[M]
	partsBySession    map[string][]durableMessageRow[M]
}

func prepareDurableMessageStore[M any](messages, parts []durableMessageRow[M],
	malformedMessages, malformedParts map[string]string) durableMessageStore[M] {
	maps.Copy(malformedParts, malformedMessages)
	return durableMessageStore[M]{
		malformed:         malformedParts,
		messagesBySession: durableRowsBySession(messages),
		partsBySession:    durableRowsBySession(parts),
	}
}

func projectDurableSessions[M any](sourceName string, sources []row,
	store durableMessageStore[M], coverage *parsers.MessageCoverage,
	project func(row, []durableMessageRow[M], []durableMessageRow[M]) (parsers.Session, int, []parsers.Discard),
) ([]parsers.Session, int, []parsers.Discard, []string) {
	var sessions []parsers.Session
	var discards []parsers.Discard
	complaints := durableMalformedComplaints(sourceName, store.malformed)
	seen := map[string]bool{}
	deferred := 0
	for _, source := range sources {
		native, complaint, skip := durableSessionReadiness(sourceName, source, store.malformed,
			len(store.messagesBySession[source.text("id")]), coverage)
		seen[native] = true
		if complaint != "" {
			complaints = append(complaints, complaint)
		}
		if skip {
			continue
		}
		session, held, excluded := project(source, store.messagesBySession[native],
			store.partsBySession[native])
		sessions = append(sessions, session)
		deferred += held
		discards = append(discards, excluded...)
	}
	countDurableMessagesWithoutSession(store.messagesBySession, seen, coverage)
	return sessions, deferred, discards, complaints
}

func durableMalformedComplaints(source string, malformed map[string]string) []string {
	var complaints []string
	for _, id := range slices.Sorted(maps.Keys(malformed)) {
		complaints = append(complaints, fmt.Sprintf("%s session %s: %s", source, id, malformed[id]))
	}
	return complaints
}

func durableSessionReadiness(sourceName string, source row, malformed map[string]string,
	messageCount int, coverage *parsers.MessageCoverage) (native, complaint string, skip bool) {
	native = source.text("id")
	if _, broken := malformed[native]; broken {
		coverage.Skipped["session contains malformed JSON"] += messageCount
		return native, "", true
	}
	if !source.has("time_created") || !source.has("time_updated") {
		coverage.Skipped["session declares no timestamps"] += messageCount
		return native, fmt.Sprintf("%s session %s: it declares no timestamps", sourceName, native), true
	}
	return native, "", false
}

func countDurableMessagesWithoutSession[M any](messages map[string][]durableMessageRow[M],
	seen map[string]bool, coverage *parsers.MessageCoverage) {
	for sessionID, unseen := range messages {
		if !seen[sessionID] {
			coverage.Skipped["message references a missing session"] += len(unseen)
		}
	}
}

func durableTextOfParts[M any](parts []durableMessageRow[M], kind string) string {
	var texts []string
	for _, part := range parts {
		if part.part.Type == kind && part.part.Text != "" {
			texts = append(texts, part.part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func durableMessageHasLiveTool[M any](parts []durableMessageRow[M]) bool {
	return slices.ContainsFunc(parts, func(part durableMessageRow[M]) bool {
		status := part.part.status()
		return part.part.Type == "tool" && (status == "pending" || status == "running")
	})
}

func populateDurableExchange(exchange *parsers.Exchange, role, humanText, agentText string,
	created, completed, rowCreated, rowUpdated *float64) {
	if role == "user" {
		exchange.HumanText = humanText
		exchange.HumanTimestamp = isoFromMS(completionOf(created, rowCreated))
		exchange.RewriteOnIdentityChange = true
		return
	}
	exchange.AgentText = agentText
	exchange.AgentTimestamp = isoFromMS(completionOf(completed, rowUpdated))
}

func addDurableMessageUsage(tally *parsers.UsageTally, cost *float64,
	tokens *durableMessageTokens) {
	if cost != nil {
		tally.AddCost(*cost)
	}
	if tokens == nil {
		return
	}
	if tokens.Input != nil || tokens.Cache != nil &&
		(tokens.Cache.Read != nil || tokens.Cache.Write != nil) {
		prompt := roundToInt(tokens.Input)
		if tokens.Cache != nil {
			prompt += roundToInt(tokens.Cache.Read) + roundToInt(tokens.Cache.Write)
		}
		tally.AddInputTokens(prompt)
	}
	if tokens.Output != nil {
		tally.AddOutputTokens(roundToInt(tokens.Output))
	}
	if tokens.Reasoning != nil {
		tally.AddReasoningTokens(roundToInt(tokens.Reasoning))
	}
}

func durablePartProjection[M any](parts []durableMessageRow[M],
	content func(durableMessagePart) string) [][]string {
	projection := make([][]string, 0, len(parts))
	for _, part := range parts {
		params, failure := part.part.toolState()
		projection = append(projection, []string{
			part.id, part.part.Type, content(part.part), part.part.Tool,
			part.part.status(), params, failure,
		})
	}
	return projection
}

func durableMessageFingerprint[M any](id string, message M, parts [][]string) string {
	encoded, err := json.Marshal(struct {
		ID      string     `json:"id"`
		Message M          `json:"message"`
		Parts   [][]string `json:"parts"`
	}{ID: id, Message: message, Parts: parts})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
