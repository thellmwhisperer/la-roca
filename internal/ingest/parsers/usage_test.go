package parsers

import "testing"

func TestUsageTallyTracksInputAndOutputPresenceIndependently(t *testing.T) {
	tests := []struct {
		name              string
		add               func(*UsageTally)
		wantInputPresent  bool
		wantOutputPresent bool
	}{
		{"input only", func(tally *UsageTally) { tally.AddInputTokens(0) }, true, false},
		{"output only", func(tally *UsageTally) { tally.AddOutputTokens(7) }, false, true},
		{"both", func(tally *UsageTally) {
			tally.AddInputTokens(3)
			tally.AddOutputTokens(0)
		}, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var tally UsageTally
			test.add(&tally)
			got := tally.Provenance("", "")
			if (got.TokensIn != nil) != test.wantInputPresent ||
				(got.TokensOut != nil) != test.wantOutputPresent {
				t.Fatalf("presence = input:%t output:%t", got.TokensIn != nil, got.TokensOut != nil)
			}
		})
	}
}

func TestPartialUsageAdaptersDoNotInventInputTokens(t *testing.T) {
	seven := 7
	tests := []struct {
		name string
		read func(*testing.T) Provenance
	}{
		{"claude", func(*testing.T) Provenance {
			var tally UsageTally
			claimClaudeUsage(&tally, &claudeMessage{Usage: &claudeUsage{Output: &seven}})
			return tally.Provenance("", "")
		}},
		{"pi", func(*testing.T) Provenance {
			pending := piPending{}
			pending.claim(&piMessage{Usage: &piUsage{Output: &seven}})
			return pending.usage.Provenance("", "")
		}},
		{"codex", func(t *testing.T) Provenance {
			content := []byte(`{"type":"session_meta","payload":{"id":"partial-usage"}}
{"type":"event_msg","payload":{"type":"user_message","message":"count the output"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"output_tokens":7}}}}
{"type":"event_msg","payload":{"type":"task_complete","last_agent_message":"seven"}}`)
			records, err := Parse(KindCodexSession, content, FileMeta{})
			if err != nil {
				t.Fatal(err)
			}
			return records.Sessions[0].Exchanges[0].Provenance
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.read(t)
			if got.TokensIn != nil || got.TokensOut == nil || *got.TokensOut != seven {
				t.Fatalf("partial usage = input:%v output:%v", got.TokensIn, got.TokensOut)
			}
		})
	}
}
