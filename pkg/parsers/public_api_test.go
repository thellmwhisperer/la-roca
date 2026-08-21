package parsers_test

import (
	"slices"
	"testing"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

func TestPublicContract(t *testing.T) {
	var (
		_ = parsers.Detect
		_ = parsers.Parse

		_ parsers.Kind
		_ parsers.FileMeta
		_ parsers.File
		_ parsers.Parser
		_ parsers.Registration
		_ parsers.Destination
		_ parsers.Records
		_ parsers.Seen
		_ parsers.MessageCoverage
		_ parsers.Discard
		_ parsers.Session
		_ parsers.Exchange
		_ parsers.Provenance
		_ parsers.UsageTally
		_ parsers.Thinking
		_ parsers.ToolUse
		_ parsers.Memory
	)

	wantFamilies := []string{
		"chatgpt_web_conversations",
		"claude_memory",
		"claude_session",
		"claude_web_conversations",
		"claude_web_design_chats",
		"claude_web_memories",
		"claude_web_projects",
		"codex_file",
		"codex_history",
		"codex_memory_aggregate",
		"codex_session",
		"cowork_audit",
		"cursor_database",
		"cursor_store",
		"glm_skill",
		"grok_session",
		"grok_session_metadata",
		"hermes_memory",
		"pi_session",
		"qwen_code",
		"session_metadata",
		"subagent",
	}

	registered := parsers.Registered()
	gotFamilies := make([]string, 0, len(registered))
	for _, family := range registered {
		gotFamilies = append(gotFamilies, family.Name)
	}
	slices.Sort(gotFamilies)
	if !slices.Equal(gotFamilies, wantFamilies) {
		t.Fatalf("registered parser families = %v, want %v", gotFamilies, wantFamilies)
	}
}
