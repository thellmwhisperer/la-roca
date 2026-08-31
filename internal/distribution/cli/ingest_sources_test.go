package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/provider/config"
)

func TestIngestSourcesReadDeclaredPaths(t *testing.T) {
	tests := []struct {
		name, key, environment, fileName string
		value                            func(zcode, bot string) string
	}{
		{"ZCode database", "zcode_db_path", "ZCODE_DB_PATH", "declared-zcode.sqlite",
			func(zcode, _ string) string { return zcode }},
		{"OpenCode Telegram bot logs", "opencode_telegram_bot_logs", "OPENCODE_TELEGRAM_BOT_LOGS",
			"declared-bot-logs", func(_, bot string) string { return bot }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := filepath.Join(t.TempDir(), test.fileName)
			t.Setenv(test.environment, "/synthetic/environment-value")
			path := filepath.Join(t.TempDir(), "config.toml")
			content := "[defaults]\n" + test.key + " = \"" + want + "\"\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := config.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			roots := ingestSources(file, "/synthetic/home", "/synthetic/runner")
			if got := test.value(roots.ZCodeDB, roots.OpenCodeTelegramLogs); got != want {
				t.Fatalf("resolved path = %q, want configured %q", got, want)
			}
		})
	}
}
