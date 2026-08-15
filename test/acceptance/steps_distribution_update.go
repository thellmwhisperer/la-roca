//go:build acceptance

package acceptance

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

func (w *distributionWorld) updateAgainstUnreachableRelease() error {
	if err := w.ensurePrepared(); err != nil {
		return err
	}
	before, err := fingerprintHome(w.home)
	if err != nil {
		return err
	}
	w.state["updateBefore"] = before
	records, err := executionLogRecords(w.home)
	if err != nil {
		return err
	}
	w.state["updateLogRecords"] = records
	durableRecords, err := durableExecutionRecords(w.home)
	if err != nil {
		return err
	}
	w.state["updateDurableRecords"] = durableRecords
	w.last = w.run("update", "--api", "http://127.0.0.1:1", "--repo", "synthetic/unreachable", "--binary", w.installed)
	return nil
}

func (w *distributionWorld) failedUpdateChangesNothing() error {
	if w.last.code == 0 {
		return fmt.Errorf("update succeeded against an unreachable release")
	}
	message := strings.ToLower(w.last.stdout + w.last.stderr)
	if !strings.Contains(message, "error:") || !strings.Contains(message, "release") || strings.Contains(message, "panic") || strings.Contains(message, "traceback") {
		return fmt.Errorf("update failure is not plain: %s", message)
	}
	after, err := fingerprintHome(w.home)
	if err != nil {
		return err
	}
	if before := w.state["updateBefore"].(map[string]string); !reflect.DeepEqual(after, before) {
		return fmt.Errorf("failed update changed the installation:\nbefore=%v\nafter=%v", before, after)
	}
	records, err := executionLogRecords(w.home)
	if err != nil {
		return err
	}
	if before := w.state["updateLogRecords"].(int); records != before+1 {
		return fmt.Errorf("failed update added %d audit records, want 1", records-before)
	}
	durableRecords, err := durableExecutionRecords(w.home)
	if err != nil {
		return err
	}
	if before := w.state["updateDurableRecords"].(int); durableRecords != before+1 {
		return fmt.Errorf("failed update added %d durable audit records, want 1", durableRecords-before)
	}
	return nil
}

func fingerprintHome(home string) (map[string]string, error) {
	result := map[string]string{}
	err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == filepath.Join(home, ".roca", "logs") {
				return filepath.SkipDir
			}
			return nil
		}
		relative, _ := filepath.Rel(home, path)
		if relative == filepath.Join(".roca", "plugins", "roca-ops", "roca-ops.db") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		result[relative] = fmt.Sprintf("%x", sum)
		return nil
	})
	return result, err
}

func durableExecutionRecords(home string) (int, error) {
	path := filepath.Join(home, ".roca", "plugins", "roca-ops", "roca-ops.db")
	database, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, err
	}
	defer database.Close()
	var total int
	if err := database.QueryRow(`SELECT COUNT(*) FROM call_history WHERE stream = 'executions'`).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func executionLogRecords(home string) (int, error) {
	files, err := filepath.Glob(filepath.Join(home, ".roca", "logs", "executions-*.jsonl"))
	if err != nil {
		return 0, err
	}
	total := 0
	for _, path := range files {
		file, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			total++
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return 0, scanErr
		}
		if closeErr != nil {
			return 0, closeErr
		}
	}
	return total, nil
}
