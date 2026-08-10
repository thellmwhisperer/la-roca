//go:build acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cucumber/godog"
)

type ingestRun struct {
	code   int
	stdout string
	stderr string
	doc    map[string]any
}

type ingestAcceptanceWorld struct {
	binary string
	home   string
	dbPath string

	last     ingestRun
	previous ingestRun

	fixturePath string
	sessionID   string
	seeded      []string

	countsBefore map[string]int
	countsAfter  map[string]int
	databaseHash string
	expected     []string
}

var supportedIngestFamilies = []string{
	"claude", "claude-desktop", "cowork", "codex", "opencode", "pi", "hermes",
}

func (w *ingestAcceptanceWorld) registerLifecycle(ctx *godog.ScenarioContext) {
	ctx.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
		root := filepath.Join("..", "..", ".tmp")
		if err := os.MkdirAll(root, 0o700); err != nil {
			return c, err
		}
		home, err := os.MkdirTemp(root, "ingest-acceptance-")
		if err != nil {
			return c, err
		}
		w.home, err = filepath.Abs(home)
		if err != nil {
			return c, err
		}
		w.dbPath = filepath.Join(home, ".roca", "roca.db")
		w.last, w.previous = ingestRun{}, ingestRun{}
		w.fixturePath, w.sessionID = "", ""
		w.seeded, w.expected = nil, nil
		w.countsBefore, w.countsAfter = nil, nil
		w.databaseHash = ""
		if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
			return c, err
		}
		result, err := w.runCommand("init", "--db-path", w.dbPath, "--json")
		if err != nil {
			return c, err
		}
		if result.code != 0 {
			return c, fmt.Errorf("initialize ingest database: code %d: %s", result.code, result.stderr)
		}
		w.last, w.previous = ingestRun{}, ingestRun{}
		return c, nil
	})
	ctx.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w.fixturePath != "" {
			_ = os.Chmod(w.fixturePath, 0o600)
		}
		return c, os.RemoveAll(w.home)
	})
}

func (w *ingestAcceptanceWorld) runCommand(args ...string) (ingestRun, error) {
	cmd := exec.Command(w.binary, args...)
	cmd.Env = []string{
		"HOME=" + w.home,
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR=" + filepath.Join(w.home, "tmp"),
	}
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	result := ingestRun{stdout: stdout.String(), stderr: stderr.String()}
	if err != nil {
		var exit *exec.ExitError
		if !asExitError(err, &exit) {
			return result, err
		}
		result.code = exit.ExitCode()
	}
	if strings.TrimSpace(result.stdout) != "" {
		_ = json.Unmarshal([]byte(result.stdout), &result.doc)
	}
	w.previous, w.last = w.last, result
	return result, nil
}

func (w *ingestAcceptanceWorld) runIngest(dryRun bool) error {
	before, err := w.tableCounts()
	if err != nil {
		return err
	}
	w.countsBefore = before
	args := []string{"ingest", "--db-path", w.dbPath, "--json"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	result, err := w.runCommand(args...)
	if err != nil {
		return err
	}
	after, countErr := w.tableCounts()
	w.countsAfter = after
	if countErr != nil {
		return countErr
	}
	if result.doc == nil {
		return fmt.Errorf("ingest output is not JSON: %s%s", result.stdout, result.stderr)
	}
	return nil
}

func loadDomainFeatures(dir string) ([]godog.Feature, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.feature"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no feature files under %s", dir)
	}
	sort.Strings(paths)
	features := make([]godog.Feature, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		features = append(features, godog.Feature{Name: path, Contents: raw})
	}
	return features, nil
}

func (w *ingestAcceptanceWorld) writeConfig(workspace string) error {
	body := ""
	if workspace != "" {
		body = fmt.Sprintf("workspace_roots = [%q]\n", workspace)
	}
	return writeFixture(filepath.Join(w.home, ".roca", "config.toml"), body)
}

func (w *ingestAcceptanceWorld) openDB() (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+w.dbPath+"?_pragma=busy_timeout(5000)")
}

func (w *ingestAcceptanceWorld) queryInt(statement string, args ...any) (int, error) {
	db, err := w.openDB()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var value int
	err = db.QueryRow(statement, args...).Scan(&value)
	return value, err
}

func (w *ingestAcceptanceWorld) queryStrings(statement string, args ...any) ([]string, error) {
	db, err := w.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (w *ingestAcceptanceWorld) tableCounts() (map[string]int, error) {
	db, err := w.openDB()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	counts := map[string]int{}
	for _, table := range []string{"memories", "sessions", "exchanges", "thinking_blocks", "tool_uses"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			return nil, err
		}
		counts[table] = count
	}
	return counts, nil
}

func (w *ingestAcceptanceWorld) databaseBytes() (string, error) {
	raw, err := os.ReadFile(w.dbPath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (w *ingestAcceptanceWorld) reportFileCounts() (int, int, int, error) {
	skipped, err := ingestJSONNumber(w.last.doc, "files_skipped")
	if err != nil {
		return 0, 0, 0, err
	}
	errors, err := ingestJSONNumber(w.last.doc, "errors")
	if err != nil {
		return 0, 0, 0, err
	}
	details, _ := w.last.doc["error_details"].([]any)
	return skipped, errors, len(details), nil
}

func ingestJSONNumber(document map[string]any, path ...string) (int, error) {
	var current any = document
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("%s is not an object", strings.Join(path, "."))
		}
		current, ok = object[key]
		if !ok {
			return 0, fmt.Errorf("missing JSON field %s", strings.Join(path, "."))
		}
	}
	value, ok := current.(float64)
	if !ok {
		return 0, fmt.Errorf("%s = %v, want number", strings.Join(path, "."), current)
	}
	return int(value), nil
}
