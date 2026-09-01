package corpuswriter

import (
	"context"
	"database/sql"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// Records are the normalized conversations to write in one transaction.
type Records struct {
	Sessions []Session
}

// Session is one conversation and everything that hangs from it.
type Session struct {
	ID                           string
	SourceAgent                  string
	SourceSurface                string
	Project                      string
	StartedAt                    string
	EndedAt                      string
	Title                        string
	DurationMinutes              *int
	Metadata                     map[string]any
	SnapshotUpdatedAt            string
	Snapshot                     bool
	HistoryFallback              bool
	ExchangeNumbersAuthoritative bool
	ParentID                     string
	AgentMayUpgrade              bool
	ExchangeKeyScope             string
	PruneUnmappedExchanges       bool
	Exchanges                    []Exchange
	// OrphanedTools are session-level calls outside completed exchanges. Nil
	// leaves existing rows untouched; a non-nil slice replaces that projection.
	OrphanedTools []ToolUse
	Thinking      []Thinking
}

// Exchange is one human turn and the agent response it received.
type Exchange struct {
	Number                  int
	SourceID                string
	Fingerprint             string
	RewriteOnIdentityChange bool
	IsAfterCompaction       bool
	HumanText               string
	AgentText               string
	HumanTimestamp          string
	AgentTimestamp          string
	LatencyMS               *int
	Thinking                []Thinking
	Tools                   []ToolUse
	Provenance              Provenance
	Signal                  *int
}

// Provenance records the model, provider, usage, and cost stated by a source.
type Provenance struct {
	Model           string
	Provider        string
	TokensIn        *int
	TokensOut       *int
	TokensReasoning *int
	CostUSD         *float64
}

// Thinking is one reasoning block attached to a session or exchange.
type Thinking struct {
	Position          float64
	Depth             string
	WordCount         int
	IsAfterCompaction bool
	Text              string
}

// ToolUse is one tool call and the verdict carried by its result.
type ToolUse struct {
	Name          string
	ParamsSummary string
	HadError      bool
	ErrorMessage  string
}

// Counts reports the rows affected by a write.
type Counts struct {
	Sessions                int `json:"sessions"`
	SessionsUpdated         int `json:"sessions_updated"`
	Exchanges               int `json:"exchanges"`
	ThinkingBlocks          int `json:"thinking_blocks"`
	ToolUses                int `json:"tool_uses"`
	ExchangesUnchanged      int `json:"exchanges_unchanged"`
	ExchangesChanged        int `json:"exchanges_changed"`
	ExchangesDeleted        int `json:"exchanges_deleted"`
	AnchorConflicts         int `json:"anchor_conflicts"`
	ThinkingBlocksDiscarded int `json:"thinking_blocks_discarded"`
}

// Write persists normalized conversations through the same insert path used by
// La Roca ingest. The caller owns committing or rolling back tx. If metadata
// enrichment meets an exact-payload collision, Write preserves the later row's
// existing metadata and continues without returning that collision as an error.
func Write(ctx context.Context, tx *sql.Tx, records Records) (Counts, error) {
	sessions := make([]parsers.Session, len(records.Sessions))
	for i, session := range records.Sessions {
		sessions[i] = internalSession(session)
	}
	written, err := ingest.WriteSessions(ctx, tx, sessions)
	return Counts{
		Sessions: written.Sessions, SessionsUpdated: written.SessionsUpdated,
		Exchanges: written.Exchanges, ThinkingBlocks: written.ThinkingBlocks,
		ToolUses: written.ToolUses, ExchangesUnchanged: written.ExchangesUnchanged,
		ExchangesChanged: written.ExchangesChanged, ExchangesDeleted: written.ExchangesDeleted,
		AnchorConflicts:         written.AnchorConflicts,
		ThinkingBlocksDiscarded: written.ThinkingBlocksDiscarded,
	}, err
}

func internalSession(session Session) parsers.Session {
	exchanges := make([]parsers.Exchange, len(session.Exchanges))
	for i, exchange := range session.Exchanges {
		exchanges[i] = internalExchange(exchange)
	}
	return parsers.Session{
		ID: session.ID, SourceAgent: session.SourceAgent, SourceSurface: session.SourceSurface,
		Project: session.Project, StartedAt: session.StartedAt, EndedAt: session.EndedAt,
		Title: session.Title, DurationMinutes: session.DurationMinutes, Metadata: session.Metadata,
		SnapshotUpdatedAt: session.SnapshotUpdatedAt, Snapshot: session.Snapshot,
		HistoryFallback:              session.HistoryFallback,
		ExchangeNumbersAuthoritative: session.ExchangeNumbersAuthoritative,
		ParentID:                     session.ParentID, AgentMayUpgrade: session.AgentMayUpgrade,
		ExchangeKeyScope:       session.ExchangeKeyScope,
		PruneUnmappedExchanges: session.PruneUnmappedExchanges,
		Exchanges:              exchanges, OrphanedTools: internalTools(session.OrphanedTools),
		Thinking: internalThinking(session.Thinking),
	}
}

func internalExchange(exchange Exchange) parsers.Exchange {
	return parsers.Exchange{
		Number: exchange.Number, SourceID: exchange.SourceID,
		Fingerprint:             exchange.Fingerprint,
		RewriteOnIdentityChange: exchange.RewriteOnIdentityChange,
		IsAfterCompaction:       exchange.IsAfterCompaction,
		HumanText:               exchange.HumanText, AgentText: exchange.AgentText,
		HumanTimestamp: exchange.HumanTimestamp, AgentTimestamp: exchange.AgentTimestamp,
		LatencyMS: exchange.LatencyMS, Thinking: internalThinking(exchange.Thinking),
		Tools: internalTools(exchange.Tools),
		Provenance: parsers.Provenance{
			Model: exchange.Provenance.Model, Provider: exchange.Provenance.Provider,
			TokensIn: exchange.Provenance.TokensIn, TokensOut: exchange.Provenance.TokensOut,
			TokensReasoning: exchange.Provenance.TokensReasoning,
			CostUSD:         exchange.Provenance.CostUSD,
		},
		Signal: exchange.Signal,
	}
}

func internalTools(tools []ToolUse) []parsers.ToolUse {
	if tools == nil {
		return nil
	}
	result := make([]parsers.ToolUse, len(tools))
	for i, tool := range tools {
		result[i] = parsers.ToolUse{
			Name: tool.Name, ParamsSummary: tool.ParamsSummary,
			HadError: tool.HadError, ErrorMessage: tool.ErrorMessage,
		}
	}
	return result
}

func internalThinking(blocks []Thinking) []parsers.Thinking {
	result := make([]parsers.Thinking, len(blocks))
	for i, block := range blocks {
		result[i] = parsers.Thinking{
			Position: block.Position, Depth: block.Depth, WordCount: block.WordCount,
			IsAfterCompaction: block.IsAfterCompaction, Text: block.Text,
		}
	}
	return result
}
