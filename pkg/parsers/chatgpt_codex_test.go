package parsers

import (
	"bytes"
	"strings"
	"testing"
)

const chatGPTCodexFixture = `[
  {
    "id": "fixture-codex-cloud",
    "title": "Synthetic GitHub mention hatch",
    "archived": false,
    "turns": [
      {
        "id": "turn-user-1",
        "role": "user",
        "custom_instructions": "Stay inside the invented lighthouse.",
        "input_items": [
          {
            "type": "message",
            "role": "user",
            "content": [{"content_type": "text", "text": "Open the imaginary hatch."}]
          },
          {
            "type": "pull_request_info",
            "title": "Synthetic PR",
            "body": "Invented pull request body",
            "base_ref": "main",
            "head_ref": "synthetic-branch",
            "merge_commit_sha": null
          }
        ]
      },
      {
        "id": "turn-asst-1",
        "role": "assistant",
        "previous_turn_id": "turn-user-1",
        "branch": "synthetic-codex-cloud",
        "branch_name": null,
        "external_pull_request_id": "pr-synthetic-1",
        "pull_request_status": "created",
        "turn_status": "TaskTurnStatusEnum.COMPLETED",
        "output_items": [
          {
            "type": "message",
            "role": "assistant",
            "content": [
              {"content_type": "text", "text": "The hatch is open."},
              {"content_type": "repo_file_citation", "path": "README.md", "line_range_start": 1, "line_range_end": 2}
            ]
          },
          {
            "type": "partial_repo_snapshot",
            "files": [{"path": "README.md", "contents": null}]
          }
        ]
      }
    ]
  }
]`

func TestChatGPTCodexPairsTurnsAndLeavesUnknownsEmpty(t *testing.T) {
	records := parseChatGPTCodex(t, chatGPTCodexFixture)
	if len(records.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(records.Sessions))
	}
	session := records.Sessions[0]
	if session.ID != "fixture-codex-cloud" || session.SourceAgent != "codex-cloud" ||
		session.Title != "Synthetic GitHub mention hatch" {
		t.Fatalf("session = %+v", session)
	}
	if session.StartedAt != "" || session.EndedAt != "" || session.DurationMinutes != nil {
		t.Fatalf("invented timestamps = %q to %q duration=%v",
			session.StartedAt, session.EndedAt, session.DurationMinutes)
	}
	if len(session.Exchanges) != 1 {
		t.Fatalf("exchanges = %+v", session.Exchanges)
	}
	exchange := session.Exchanges[0]
	if exchange.HumanText != "Open the imaginary hatch." || exchange.AgentText != "The hatch is open." {
		t.Fatalf("exchange texts = %q / %q", exchange.HumanText, exchange.AgentText)
	}
	if strings.Contains(exchange.HumanText, "Invented pull request body") {
		t.Fatal("pull_request_info body was treated as the human turn")
	}
	if strings.Contains(exchange.AgentText, "README.md") {
		t.Fatal("repo file citation was treated as the agent answer")
	}
	if exchange.HumanTimestamp != "" || exchange.AgentTimestamp != "" || exchange.LatencyMS != nil {
		t.Fatalf("invented exchange times = %+v", exchange)
	}
	if exchange.Provenance.Model != "" || exchange.Provenance.Provider != "" ||
		exchange.Provenance.TokensIn != nil || exchange.Provenance.TokensOut != nil {
		t.Fatalf("invented provenance = %+v", exchange.Provenance)
	}
	if exchange.SourceID != "turn-asst-1" {
		t.Errorf("source id = %q, want the assistant turn id", exchange.SourceID)
	}
	if exchange.Signal == nil || *exchange.Signal == 0 {
		t.Fatalf("signal = %v, want a measured count of what the snapshot stated", exchange.Signal)
	}
	if session.Metadata["archived"] != false {
		t.Errorf("archived metadata = %+v", session.Metadata)
	}
}

func TestChatGPTCodexDetectsDeclaredCloudSourceAndRejectsLocalRollouts(t *testing.T) {
	cloud := File{Content: []byte(chatGPTCodexFixture), Meta: FileMeta{SourceAgent: "codex-cloud"}}
	if !detectChatGPTCodex(cloud) {
		t.Fatal("detector rejected its declared cloud Codex export")
	}
	local := File{
		Content: []byte(`{"type":"session_meta","payload":{"id":"fixture-codex"}}`),
		Meta:    FileMeta{SourceAgent: "codex"},
	}
	if detectChatGPTCodex(local) {
		t.Fatal("detector claimed a local Codex rollout")
	}
	chatgpt := File{
		Content: []byte(`[{"conversation_id":"x","mapping":{}}]`),
		Meta:    FileMeta{SourceAgent: "chatgpt-web"},
	}
	if detectChatGPTCodex(chatgpt) {
		t.Fatal("detector claimed a ChatGPT mapping export")
	}
	for name, file := range map[string]File{
		"undeclared": {Content: []byte(`[{"id":"x","turns":[]}]`)},
		"id only": {
			Content: []byte(`[{"id":"x"}]`), Meta: FileMeta{SourceAgent: "codex-cloud"},
		},
		"turns only": {
			Content: []byte(`[{"turns":[]}]`), Meta: FileMeta{SourceAgent: "codex-cloud"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if detectChatGPTCodex(file) {
				t.Fatal("detector claimed an incomplete declaration")
			}
		})
	}
}

func TestChatGPTCodexUsesOptionalTimestampsWhenTheSourceStatesThem(t *testing.T) {
	raw := `[
	  {"id":"timed","title":"Timed","archived":false,"create_time":1767225600.125,"turns":[
	    {"id":"u","role":"user","create_time":1767225601.125,"input_items":[
	      {"type":"message","role":"user","content":[{"content_type":"text","text":"Ping."}]}
	    ]},
	    {"id":"a","role":"assistant","previous_turn_id":"u","create_time":1767225602.625,"output_items":[
	      {"type":"message","role":"assistant","content":[{"content_type":"text","text":"Pong."}]}
	    ]}
	  ]}
	]`
	session := parseChatGPTCodex(t, raw).Sessions[0]
	if session.StartedAt != "2026-01-01T00:00:00.125Z" {
		t.Fatalf("session start = %q", session.StartedAt)
	}
	exchange := session.Exchanges[0]
	if exchange.HumanTimestamp != "2026-01-01T00:00:01.125Z" ||
		exchange.AgentTimestamp != "2026-01-01T00:00:02.625Z" {
		t.Fatalf("exchange timestamps = %q / %q", exchange.HumanTimestamp, exchange.AgentTimestamp)
	}
}

func TestChatGPTCodexDiscardsEmptyAssistantTurnsWithoutPoisoningTheFile(t *testing.T) {
	raw := `[
	  {"id":"ok","title":"Kept","archived":false,"turns":[
	    {"id":"u1","role":"user","input_items":[{"type":"message","role":"user","content":[{"content_type":"text","text":"Keep me."}]}]},
	    {"id":"a1","role":"assistant","previous_turn_id":"u1","output_items":[
	      {"type":"message","role":"assistant","content":[{"content_type":"text","text":"Kept."}]}
	    ]}
	  ]},
	  {"id":"failed","title":"Failed","archived":false,"turns":[
	    {"id":"u2","role":"user","input_items":[{"type":"message","role":"user","content":[{"content_type":"text","text":"This failed."}]}]},
	    {"id":"a2","role":"assistant","previous_turn_id":"u2","turn_status":"TaskTurnStatusEnum.FAILED","output_items":[]}
	  ]}
	]`
	records := parseChatGPTCodex(t, raw)
	if len(records.Sessions) != 2 {
		t.Fatalf("sessions = %d, want both conversations", len(records.Sessions))
	}
	if len(records.Sessions[0].Exchanges) != 1 || records.Sessions[0].Exchanges[0].AgentText != "Kept." {
		t.Fatalf("kept session = %+v", records.Sessions[0].Exchanges)
	}
	if len(records.Sessions[1].Exchanges) != 0 {
		t.Fatalf("failed empty assistant became an exchange: %+v", records.Sessions[1].Exchanges)
	}
	if len(records.Discards) == 0 {
		t.Fatal("empty assistant turn was silently dropped")
	}
	for _, discard := range records.Discards {
		if !discard.ByDesign {
			t.Errorf("empty assistant reported as unreadable: %+v", discard)
		}
	}
}

func TestChatGPTCodexDoesNotPairTurnsWithBlankIDs(t *testing.T) {
	raw := `[{"id":"blank-turns","turns":[
	  {"id":"","role":"user","input_items":[{"type":"message","content":[{"content_type":"text","text":"Unlinked human."}]}]},
	  {"id":"blank-assistant","role":"assistant","previous_turn_id":"","output_items":[{"type":"message","content":[{"content_type":"text","text":"Unlinked answer."}]}]},
	  {"id":"linked-user","role":"user","input_items":[{"type":"message","content":[{"content_type":"text","text":"Linked human."}]}]},
	  {"id":"linked-assistant","role":"assistant","previous_turn_id":"linked-user","output_items":[{"type":"message","content":[{"content_type":"text","text":"Linked answer."}]}]}
	]}]`
	exchanges := parseChatGPTCodex(t, raw).Sessions[0].Exchanges
	if len(exchanges) != 1 || exchanges[0].HumanText != "Linked human." ||
		exchanges[0].AgentText != "Linked answer." {
		t.Fatalf("exchanges = %+v, want only the explicitly linked turn", exchanges)
	}
}

func TestChatGPTCodexMalformedConversationDoesNotPoisonExport(t *testing.T) {
	raw := `[{"id":"ok","title":"Ok","turns":[
	    {"id":"u","role":"user","input_items":[{"type":"message","content":[{"content_type":"text","text":"Hello."}]}]},
	    {"id":"a","role":"assistant","previous_turn_id":"u","output_items":[{"type":"message","content":[{"content_type":"text","text":"Hi."}]}]}
	  ]}, 3]`
	records, err := ParseChatGPTCodex(bytes.NewReader([]byte(raw)), FileMeta{SourceAgent: "codex-cloud"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(records.Sessions) != 1 || records.Sessions[0].ID != "ok" {
		t.Fatalf("sessions = %+v", records.Sessions)
	}
	if len(records.Discards) == 0 {
		t.Fatal("unreadable envelope was not discarded")
	}
}

func parseChatGPTCodex(t *testing.T, raw string) Records {
	t.Helper()
	records, err := ParseChatGPTCodex(bytes.NewReader([]byte(raw)), FileMeta{
		Path: "/synthetic/codex.json", FileName: "codex.json", SourceAgent: "codex-cloud",
	})
	if err != nil {
		t.Fatal(err)
	}
	return records
}
