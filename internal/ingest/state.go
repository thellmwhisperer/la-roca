package ingest

import (
	"encoding/json"

	"github.com/thellmwhisperer/la-roca/pkg/incrementality"
	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// parserVersions is the reading each source kind currently gets. The version
// travels inside the watermark, so a build that learned to read more of a source
// re-reads the files it already synced instead of trusting a fingerprint earned
// by a poorer reading. What actually lands is still decided record by record:
// the shared writer matches historical turns before additive enrichment, refuses
// conflicting or ambiguous anchors, and leaves the unique index as the final
// duplicate guard. The provenance backfill only fills columns that are NULL, so
// a plain `roca ingest` enriches a corpus without writing a second copy of it.
//
// A kind absent from here declares its reading in its own registry line, or has
// no reading to declare because it never changed since the watermark was
// introduced, in which case its files stay skipped. The registry line is where a
// contributed parser always declares it, so the contribution kit stays one
// fixture folder, one parser file and one registry line.
var parserVersions = map[parsers.Kind]string{
	parsers.KindClaudeMemory:            "claude-memory-v2",
	parsers.KindClaudeSession:           "claude-session-v6",
	parsers.KindCoworkAudit:             "cowork-audit-v6",
	parsers.KindSubagent:                "subagent-v6",
	parsers.KindCodexSession:            "codex-session-v8",
	parsers.KindCodexHistory:            "codex-history-v2",
	parsers.KindPiSession:               "pi-session-v7",
	parsers.KindOpenCodeDB:              "opencode-v9",
	parsers.KindHermesDB:                "hermes-v8",
	parsers.KindClaudeWebConversations:  "claude-web-conversations-v4",
	parsers.KindClaudeWebMemories:       "claude-web-memories-v1",
	parsers.KindClaudeWebProjects:       "claude-web-projects-v1",
	parsers.KindClaudeWebDesignChats:    "claude-web-design-chats-v1",
	parsers.KindChatGPTWebConversations: "chatgpt-web-conversations-v4",
	parsers.KindGrokSession:             "grok-session-v4",
	parsers.KindGrokSessionMetadata:     "grok-session-metadata-v1",
}

// registeredParser is the compiled-in catalogue, bound here so the watermark can
// be exercised against a contributed registration without shipping one.
var registeredParser = parsers.Lookup

func incrementalityTarget(target Target) incrementality.Target {
	version, versioned := parserVersions[target.Kind]
	if !versioned {
		if registered, found := registeredParser(string(target.Kind)); found {
			version = registered.Version
		}
	}
	return incrementality.Target{
		Path:          target.Path,
		Kind:          string(target.Kind),
		SourceAgent:   target.SourceAgent,
		Project:       target.Project,
		ParserVersion: version,
		IncludeSQLiteWAL: target.Kind == parsers.KindOpenCodeDB ||
			target.Kind == parsers.KindHermesDB || target.Kind == parsers.KindCursorDB ||
			target.Kind == parsers.KindCursorStore,
		CompanionPaths: target.CompanionPaths,
	}
}

func targetFingerprint(target Target) (string, error) {
	return incrementality.TargetFingerprint(incrementalityTarget(target))
}

func stateMessageCoverage(state incrementality.FileState) *parsers.MessageCoverage {
	var summary struct {
		MessageCoverage *parsers.MessageCoverage `json:"message_coverage"`
	}
	_ = json.Unmarshal(state.Metadata, &summary)
	return summary.MessageCoverage
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
