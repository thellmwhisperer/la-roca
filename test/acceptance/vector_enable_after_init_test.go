//go:build acceptance

package acceptance

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cucumber/godog"
	_ "modernc.org/sqlite"
)

func registerVectorEnableAfterInitSteps(ctx *godog.ScenarioContext, m *world) {
	var server *httptest.Server
	downloaded := make(chan struct{}, 1)
	ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if server != nil {
			server.Close()
			server = nil
		}
		return c, nil
	})
	ctx.Given(`^the real vector companion uses a local model download fixture$`, func() error {
		plugin := filepath.Join(m.home, ".roca", "plugins", "roca-vector")
		if _, err := os.Stat(plugin); !os.IsNotExist(err) {
			return fmt.Errorf("vector-off init must leave no managed vector directory: %v", err)
		}
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Setup is asynchronous. Keep the model unavailable without downloading
			// a real model or contacting the release service.
			http.Error(w, "local model fixture unavailable", http.StatusServiceUnavailable)
			select {
			case downloaded <- struct{}{}:
			default:
			}
		}))
		binary := filepath.Join(m.home, "bin", "roca-vector")
		args := []string{"build", "-ldflags", "-X github.com/thellmwhisperer/la-roca-vector/internal/model.DownloadURL=" + server.URL, "-o", binary}
		if os.Getenv("GOCOVERDIR") != "" {
			args = append(args, "-cover", "-coverpkg=./...")
		}
		build := exec.Command("go", append(args, "./cmd/roca-vector")...)
		build.Dir = filepath.Join("..", "..", "plugins", "vector")
		build.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := build.CombinedOutput(); err != nil {
			return fmt.Errorf("build the real vector companion: %w: %s", err, output)
		}
		return m.shortResidentSockets()
	})
	ctx.Given(`^the corpus vector sidecar records a dimensions value that is not a number$`, func() error {
		path := filepath.Join(m.home, ".roca", "plugins", "roca-corpus", "roca-corpus.vector.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
			INSERT INTO meta(key,value) VALUES ('owner','roca-corpus/corpus'),('model','m'),('dimensions','eight')`)
		return err
	})
	ctx.Then(`^the managed vector state directory exists after the local model download$`, func() error {
		state := filepath.Join(m.home, ".roca", "plugins", "roca-vector", "state")
		if info, err := os.Stat(state); err != nil || !info.IsDir() {
			return fmt.Errorf("managed vector state directory missing: %v", err)
		}
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		select {
		case <-downloaded:
		case <-deadline.C:
			return fmt.Errorf("background setup did not request the local model fixture")
		}
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			if _, err := os.Stat(filepath.Join(state, ".worker")); os.IsNotExist(err) {
				return nil
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				return fmt.Errorf("background setup did not finish after the local model fixture")
			}
		}
	})
}
