// Package parsers is the pure half of the ingest: it turns one artefact's bytes
// into normalized records and nothing else.
// File-format parsers live here; store-backed readers such as OpenCode and Hermes stay in internal/ingest beside foreign-source guards.
//
// It touches neither the database nor the clock nor the disk beyond the content
// it is handed. That isolation makes the ingest
// suite a table of cases with example files and expected output, with no
// database and no integration marker, which is the difference between a
// 40-second suite and a 40-minute one.
//
// Writing lives apart, in internal/ingest, which is the only place that knows
// SQL.
package parsers

import (
	"fmt"
	"strings"
)

// Kind names one artefact shape. It is what decides which parser reads a file
// and which source agent its rows are attributed to, so a foreign transcript is
// never parsed as a Claude one merely because it also ends in `.jsonl`.
type Kind string

// The source matrix names every supported artefact shape.
const (
	// KindClaudeSession is a Claude Code transcript under ~/.claude/projects.
	KindClaudeSession Kind = "claude_session"
	// KindClaudeMemory is a per-project memory file with YAML frontmatter.
	KindClaudeMemory Kind = "claude_memory"
	// KindSessionMetadata is the structured JSON of Claude Desktop and Cowork.
	KindSessionMetadata Kind = "session_metadata"
	// KindCoworkAudit is the transcript paired with a Cowork metadata file.
	KindCoworkAudit Kind = "cowork_audit"
	// KindCodexSession is a Codex rollout.
	KindCodexSession Kind = "codex_session"
	// KindCodexFile is a Codex memory or rule.
	KindCodexFile Kind = "codex_file"
	// KindCodexMemoryAggregate is Codex's merged raw memory export. Unlike an
	// ordinary memory file, it contains one independently addressable record per
	// thread block.
	KindCodexMemoryAggregate Kind = "codex_memory_aggregate"
	// KindSubagent is a subagent transcript under a Claude project.
	KindSubagent Kind = "subagent"
	// KindPiSession is a Pi v3 session file.
	KindPiSession Kind = "pi_session"
	// KindOpenCodeDB and KindHermesDB are read by internal/ingest, which opens
	// their databases read-only. They are declared here so that one enumeration
	// names every source the ingest state table can hold.
	KindOpenCodeDB Kind = "opencode_database"
	KindHermesDB   Kind = "hermes_database"
	// The official Anthropic data export contributes its conversations and
	// Claude-web memories as separate file-state targets.
	KindClaudeWebConversations Kind = "claude_web_conversations"
	KindClaudeWebMemories      Kind = "claude_web_memories"
	// The official OpenAI data export contributes its ChatGPT mapping tree.
	KindChatGPTWebConversations Kind = "chatgpt_web_conversations"
)

// FileMeta is what the scan already knows about an artefact: where it came from
// and the identity it declared for it. A parser overrides a declared value only
// with something the content itself says.
type FileMeta struct {
	Path        string
	FileName    string
	SessionID   string
	Project     string
	SourceAgent string
	// Sidecar is the paired metadata JSON of a Cowork audit transcript. It
	// travels as content so the parser stays off the disk.
	Sidecar []byte
	// SourceType qualifies a Codex file as a memory or rule.
	SourceType string
}

// Records are the normalized rows one artefact produced. Empty is not an error:
// an empty memory file and a transcript with no complete exchange are both
// normal, and both are simply nothing to write.
type Records struct {
	Sessions []Session
	Memories []Memory
	// Discards are source records the parser deliberately left out, each with
	// the record position and the reason it could not be normalized.
	Discards []Discard
	// Deferred counts the turns the artefact is still writing. They are not
	// errors either, and they are reported so an operator can tell "nothing new"
	// from "half a session in flight".
	Deferred int
}

type Discard struct {
	Record   int    `json:"record"`
	Reason   string `json:"reason"`
	Category string `json:"category,omitempty"`
	// ByDesign marks a source record this build never intended to normalize:
	// runtime machinery a log writes about itself. It is reported apart from the
	// records that could not be read, because an operator reading "thousands of
	// discards" cannot tell a healthy ingest from a broken one otherwise.
	ByDesign bool `json:"by_design,omitempty"`
}

// Excluded is a record left out by design, named by the category an operator
// reads rather than by its position.
func Excluded(reason string) Discard { return Discard{Reason: reason, ByDesign: true} }

// Session is one conversation, with everything that hangs off it.
type Session struct {
	ID          string
	SourceAgent string
	Project     string
	StartedAt   string
	EndedAt     string
	Title       string
	// DurationMinutes is nil when either end of the session is unknown.
	DurationMinutes   *int
	Metadata          map[string]any
	SnapshotUpdatedAt string
	// Snapshot merges an observed state into the row that is already there:
	// non-null fields win and the first non-blank title stays. A metadata
	// artefact is a snapshot; re-parsing a grown transcript is not, because
	// there the identity fields only fill NULLs.
	Snapshot bool
	// ParentID is the session that spawned this one, when the artefact declares
	// it.
	ParentID string
	// AgentMayUpgrade lets a source that has learned a more precise agent name
	// replace the generic one it wrote before, inside its own family: a Codex
	// session filed as `codex` becomes `codex-<nickname>` once the state database
	// is read. Without it a re-ingest could only fill a NULL, and the nickname
	// would never land.
	AgentMayUpgrade bool
	// ExchangeKeyScope names where the source's exchange map lives inside the
	// session metadata; empty is the top level. Existing databases may carry
	// those maps in a particular
	// place, and reading them anywhere else would renumber exchanges that already
	// landed.
	ExchangeKeyScope string
	Exchanges        []Exchange
	// Thinking are the blocks that hang off the session and not off an exchange:
	// a subagent compact summary is the only one.
	Thinking []Thinking
}

// Exchange is one human turn with the agent response it got.
type Exchange struct {
	// Number is the exchange's identity inside its session, and with the session
	// id it is the natural key the unique index defends.
	Number int
	// SourceID is the source's own identity for this exchange, used by the
	// adapters whose numbering cannot be derived from the file alone (Pi's
	// active branch, OpenCode's message graph). When it is set, the writer keys
	// on it through the session metadata instead of on Number.
	SourceID string
	// Fingerprint is the hash of the source projection, so an exchange that
	// already landed is not rewritten when it did not change.
	Fingerprint       string
	IsAfterCompaction bool
	HumanText         string
	AgentText         string
	HumanTimestamp    string
	AgentTimestamp    string
	LatencyMS         *int
	Thinking          []Thinking
	Tools             []ToolUse
	// Provenance is what the source recorded about how this answer was produced.
	Provenance Provenance
}

// Provenance is the unified shape every source fills with whatever it recorded
// about one answer: who produced it and what it spent. Every field is optional
// and absence is the normal case, because not every source records all of them
// and guessing one would put an invented number in the corpus.
//
// The pointers are what tells "the source says zero" from "the source does not
// say", and only the first of those is a measurement.
type Provenance struct {
	Model           string
	Provider        string
	TokensIn        *int
	TokensOut       *int
	TokensReasoning *int
	CostUSD         *float64
}

// Empty says the source recorded nothing about this answer, which is what a
// signal-poor export leaves behind.
func (p Provenance) Empty() bool {
	return p.Model == "" && p.Provider == "" && p.TokensIn == nil &&
		p.TokensOut == nil && p.TokensReasoning == nil && p.CostUSD == nil
}

// Tokens is a token count the source stated, nil when it stated none. It is the
// one constructor for the counted fields so no adapter invents a zero.
func Tokens(value int, stated bool) *int {
	if !stated {
		return nil
	}
	return &value
}

// Cost is the same for a dollar amount.
func Cost(value float64, stated bool) *float64 {
	if !stated {
		return nil
	}
	return &value
}

// UsageTally adds up what a source stated about one turn across the several
// requests a turn is made of. Tokens and cost are tallied apart because a source
// may state one and not the other, and a total nobody stated has to stay absent
// rather than become a zero.
type UsageTally struct {
	inputStated     bool
	input           int
	outputStated    bool
	output          int
	reasoningStated bool
	reasoning       int
	costStated      bool
	cost            float64
}

// AddInputTokens records one request's prompt tokens.
func (t *UsageTally) AddInputTokens(input int) {
	t.inputStated = true
	t.input += input
}

// AddOutputTokens records one request's answer tokens.
func (t *UsageTally) AddOutputTokens(output int) {
	t.outputStated = true
	t.output += output
}

// AddReasoningTokens is apart from the input and output counters because a source that counts
// tokens does not necessarily separate the ones spent thinking, and a zero
// nobody stated would read as an answer given without thought.
func (t *UsageTally) AddReasoningTokens(count int) {
	t.reasoningStated = true
	t.reasoning += count
}

// AddCost records one request's dollar amount.
func (t *UsageTally) AddCost(amount float64) {
	t.costStated = true
	t.cost += amount
}

// Provenance is the tally as the exchange stores it, under the model and
// provider the source named.
func (t UsageTally) Provenance(model, provider string) Provenance {
	return Provenance{
		Model:           model,
		Provider:        provider,
		TokensIn:        Tokens(t.input, t.inputStated),
		TokensOut:       Tokens(t.output, t.outputStated),
		TokensReasoning: Tokens(t.reasoning, t.reasoningStated),
		CostUSD:         Cost(t.cost, t.costStated),
	}
}

// Thinking is one reasoning block.
type Thinking struct {
	Position          float64
	Depth             string
	WordCount         int
	IsAfterCompaction bool
	Text              string
}

// ToolUse is one tool call, with the verdict its result carried.
type ToolUse struct {
	Name          string
	ParamsSummary string
	HadError      bool
	ErrorMessage  string
}

// Memory is one curated text: a memory file, a rule or a skill.
type Memory struct {
	Layer   string
	Content string
	// Origin is 'cron' for everything the ingest writes: nobody typed it.
	Origin      string
	SourceAgent string
	Project     string
	Metadata    map[string]any
	// Source and FilePath are the pair that makes re-ingesting a file update its
	// memory instead of duplicating it. They also travel inside Metadata as
	// `_cron_source` and `file_path`, which identifies rows across re-ingests.
	Source   string
	FilePath string
	// CreatedAt is the source timestamp when an artefact declares one. The v1
	// memory schema has no separate updated_at column, so aggregate updates use
	// this timestamp as the row's searchable time.
	CreatedAt string
}

// byKind is the whole matrix of file artefacts: one kind, one function that
// decodes it. The two database kinds are absent because internal/ingest reads
// them itself, and a kind with no entry here is a refusal by name.
var byKind = map[Kind]func([]byte, FileMeta) (Records, error){
	KindClaudeSession:        ParseClaudeSession,
	KindCoworkAudit:          ParseCoworkAudit,
	KindSessionMetadata:      ParseSessionMetadata,
	KindCodexSession:         ParseCodexSession,
	KindSubagent:             ParseSubagent,
	KindPiSession:            ParsePiSession,
	KindClaudeMemory:         ParseClaudeMemory,
	KindCodexFile:            ParseCodexFile,
	KindCodexMemoryAggregate: ParseCodexMemoryAggregate,
	KindClaudeWebConversations: func(content []byte, meta FileMeta) (Records, error) {
		return ParseClaudeWebConversations(strings.NewReader(string(content)), meta)
	},
	KindClaudeWebMemories: func(content []byte, meta FileMeta) (Records, error) {
		return ParseClaudeWebMemories(strings.NewReader(string(content)), meta)
	},
	KindChatGPTWebConversations: func(content []byte, meta FileMeta) (Records, error) {
		return ParseChatGPTWebConversations(strings.NewReader(string(content)), meta)
	},
}

// Parse turns an artefact into normalized records. It does not open the
// database, it does not consult the clock, and it is deterministic: same
// content, same result.
func Parse(kind Kind, content []byte, meta FileMeta) (Records, error) {
	parse, known := byKind[kind]
	if !known {
		return Records{}, fmt.Errorf("there is no parser for source kind %q", kind)
	}
	return parse(content, meta)
}

// lines splits a JSONL artefact, dropping the blank lines. A line that is not
// valid JSON is skipped by each parser, never fatal: one corrupt line in a live
// transcript cannot cost the whole file.
func lines(content []byte) []string {
	raw := strings.Split(string(content), "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// wordCount counts whitespace-separated words.
func wordCount(text string) int { return len(strings.Fields(text)) }

// PlaceThinking gives every thinking block its position in the session, which is
// its exchange's number over how many exchanges the session turned out to have.
// No parser can know that until it has read the last line, so all of them do it
// at the end, and the adapters that read a live database do it too.
func PlaceThinking(exchanges []Exchange) {
	total := float64(max(len(exchanges), 1))
	for i := range exchanges {
		for k := range exchanges[i].Thinking {
			exchanges[i].Thinking[k].Position = float64(exchanges[i].Number) / total
		}
	}
}

// Clip cuts a summary to a budget in runes, never in bytes: cutting a text in
// the middle of a rune would put invalid UTF-8 in the database. It is exported
// because the source adapters that read a live database clip the same way.
func Clip(text string, budget int) string {
	runes := []rune(text)
	if len(runes) <= budget {
		return text
	}
	return string(runes[:budget])
}
