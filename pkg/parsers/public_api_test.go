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

func TestPublicRegistryCopiesMutableRouting(t *testing.T) {
	type getter func(string) (parsers.Registration, bool)
	registered := getter(func(name string) (parsers.Registration, bool) {
		for _, registration := range parsers.Registered() {
			if registration.Name == name {
				return registration, true
			}
		}
		return parsers.Registration{}, false
	})

	fields := []struct {
		name   string
		values func(parsers.Registration) []string
	}{
		{name: "locations", values: func(registration parsers.Registration) []string {
			return registration.Locations
		}},
		{name: "harvest locations", values: func(registration parsers.Registration) []string {
			return registration.HarvestLocations
		}},
	}
	getters := []struct {
		name string
		get  getter
	}{
		{name: "Registered", get: registered},
		{name: "Lookup", get: parsers.Lookup},
	}

	for _, field := range fields {
		var registration parsers.Registration
		for _, candidate := range parsers.Registered() {
			if len(field.values(candidate)) > 0 {
				registration = candidate
				break
			}
		}
		if registration.Name == "" {
			t.Fatalf("registry has no registration with %s", field.name)
		}

		for _, accessor := range getters {
			t.Run(accessor.name+"/"+field.name, func(t *testing.T) {
				first, ok := accessor.get(registration.Name)
				if !ok {
					t.Fatalf("registration %q not found", registration.Name)
				}
				original := field.values(first)[0]
				field.values(first)[0] = "not-a-route"

				second, ok := accessor.get(registration.Name)
				if !ok {
					t.Fatalf("registration %q not found", registration.Name)
				}
				if got := field.values(second)[0]; got != original {
					t.Fatalf("%s returned aliased %s: got %q, want %q", accessor.name, field.name, got, original)
				}
			})
		}
	}
}
