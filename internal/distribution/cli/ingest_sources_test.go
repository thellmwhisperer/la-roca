package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestIngestSourcesReadsOpenCodeTelegramBotLogConfig(t *testing.T) {
	want := filepath.Join(t.TempDir(), "declared-bot-logs")
	t.Setenv("OPENCODE_TELEGRAM_BOT_LOGS", "/synthetic/environment-bot-logs")
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "[defaults]\nopencode_telegram_bot_logs = \"" + want + "\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := ingestSources(file, "/synthetic/home", "/synthetic/runner").OpenCodeTelegramLogs
	if got != want {
		t.Fatalf("OpenCode Telegram bot logs = %q, want configured %q", got, want)
	}
}
