package provider

import (
	"fmt"
	"strings"
	"testing"
)

// retiredSQLTokenCap is the former DefaultMaxTokens budget. A realistic
// federated UNION already exceeded it and died mid-statement; these tests
// lock that the transport no longer imposes that ceiling.
const retiredSQLTokenCap = 500

// longSQLSelect is the live failure shape: a multi-database UNION with long
// COALESCE chains, longer than the retired 500-token generation cap.
func longSQLSelect() string {
	const branches = 48
	var b strings.Builder
	for i := 0; i < branches; i++ {
		if i > 0 {
			b.WriteString("UNION ALL\n")
		}
		fmt.Fprintf(&b, "SELECT COALESCE(plugin_%[1]d.receipts.title, plugin_%[1]d.receipts.body, plugin_%[1]d.receipts.note, plugin_%[1]d.receipts.path, plugin_%[1]d.receipts.label) AS text, 'plugin-%[1]d' AS \"database\" FROM plugin_%[1]d.receipts\n", i)
	}
	b.WriteString("LIMIT 10")
	return b.String()
}

func TestALongSelectSurvivesEveryProviderPathIntact(t *testing.T) {
	sql := longSQLSelect()
	if got := strings.Count(sql, " ") + 1; got <= retiredSQLTokenCap {
		t.Fatalf("fixture is not longer than the retired cap: ~%d tokens", got)
	}

	t.Run("http ollama omits optional num_predict and returns the statement", func(t *testing.T) {
		res, posted := ollamaPosted(t, ChatRequest{
			Messages: []Message{{Role: RoleUser, Content: "latest handoff for synthetic-orchid"}},
		}, sql, nil)
		if res.Content != sql {
			t.Fatalf("HTTP transport clipped the SELECT: got %d bytes, want %d", len(res.Content), len(sql))
		}
		options, _ := posted["options"].(map[string]any)
		if _, present := options["num_predict"]; present {
			t.Fatalf("Ollama SQL generation still sent num_predict=%v; the field is optional and must be omitted", options["num_predict"])
		}
	})

	t.Run("http ollama still sends the probe's one-token cap", func(t *testing.T) {
		_, posted := ollamaPosted(t, ChatRequest{
			Messages:  []Message{{Role: RoleUser, Content: "Reply with one character."}},
			MaxTokens: 1,
		}, "x", nil)
		options, _ := posted["options"].(map[string]any)
		if options["num_predict"] != float64(1) {
			t.Fatalf("probe num_predict = %v, want 1", options["num_predict"])
		}
	})

	t.Run("cli localbinary returns the statement", func(t *testing.T) {
		t.Setenv("FAKE_PROVIDER_MODE", "longsql")
		t.Setenv("FAKE_PROVIDER_SQL", sql)
		binary, err := NewLocalBinary(LocalBinaryConfig{
			Name: "fixture", Command: []string{fakeBinary(t)},
			Model: "fixture-model", WorkDir: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		answer, err := binary.Chat(t.Context(), ChatRequest{
			Messages: []Message{{Role: RoleUser, Content: "latest handoff for synthetic-orchid"}},
		})
		if err != nil || answer.Content != sql {
			t.Fatalf("CLI extraction clipped the SELECT: answer=%d bytes err=%v, want %d",
				len(answer.Content), err, len(sql))
		}
	})
}

func TestShippedCLIPresetsDoNotCapGenerationLength(t *testing.T) {
	banned := []string{"max-tokens", "max_tokens", "num_predict", "max-output", "max_output"}
	for _, name := range CommandPresetNames() {
		joined := strings.Join(commandPresets[name].Command, " ")
		for _, flag := range banned {
			if strings.Contains(joined, flag) {
				t.Errorf("preset %s still passes %s: %s", name, flag, joined)
			}
		}
	}
}
