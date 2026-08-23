// Package telemetry records local-only operational facts about the embedding
// engine as rotated JSONL under the selected data directory's logs area.
// It never stores query or document text, and it never writes a database.
package telemetry

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	KindLoad    = "load"
	KindPrewarm = "prewarm"
	KindEmbed   = "embed"
	KindBatch   = "batch"
	KindError   = "error"
	Stream      = "engine"
	LogsDir     = "logs"
	maxFiles    = 6
)

type Record struct {
	Timestamp  time.Time `json:"timestamp"`
	Kind       string    `json:"kind"`
	Backend    string    `json:"backend,omitempty"`
	Fallback   string    `json:"fallback_reason,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	BatchSize  int       `json:"batch_size,omitempty"`
	Throughput float64   `json:"throughput,omitempty"`
	MemoryHWM  int64     `json:"memory_hwm_bytes,omitempty"`
	Err        string    `json:"error,omitempty"`
}

type Store struct {
	dir          string
	now          func() time.Time
	maxFileBytes int64
	maxFiles     int
	mu           sync.Mutex
}

func Dir(dataDir string) string {
	return filepath.Join(dataDir, LogsDir)
}

func Open(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, fmt.Errorf("engine log directory is required")
	}
	dir := Dir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create the engine log directory: %w", err)
	}
	return &Store{dir: dir, now: time.Now, maxFileBytes: 5 << 20, maxFiles: maxFiles}, nil
}

func (s *Store) Close() error { return nil }

func (s *Store) Record(_ context.Context, record Record) error {
	if s == nil {
		return nil
	}
	if record.Kind == "" {
		return fmt.Errorf("engine log kind is required")
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = s.now().UTC()
	} else {
		record.Timestamp = record.Timestamp.UTC()
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode the engine log: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, Stream+"-"+record.Timestamp.Format(time.DateOnly)+".jsonl")
	if err := s.rotate(path, int64(len(line)+1)); err != nil {
		return fmt.Errorf("rotate the engine log: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open the engine log: %w", err)
	}
	_, writeErr := file.Write(append(line, '\n'))
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append the engine log: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close the engine log: %w", closeErr)
	}
	s.prune(record.Timestamp)
	return nil
}

func (s *Store) Read() ([]Record, error) {
	if s == nil {
		return nil, fmt.Errorf("engine log is closed")
	}
	matches, err := filepath.Glob(filepath.Join(s.dir, Stream+"-*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	var records []Record
	for _, path := range matches {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var record Record
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				file.Close()
				return nil, fmt.Errorf("read the engine log: %w", err)
			}
			records = append(records, record)
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return records, nil
}

func (s *Store) rotate(path string, incoming int64) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size() == 0 || info.Size()+incoming <= s.maxFileBytes {
		return nil
	}
	stem := strings.TrimSuffix(path, ".jsonl")
	for sequence := 1; ; sequence++ {
		archive := fmt.Sprintf("%s-%d.jsonl", stem, sequence)
		if _, err := os.Stat(archive); os.IsNotExist(err) {
			return os.Rename(path, archive)
		} else if err != nil {
			return err
		}
	}
}

func (s *Store) prune(now time.Time) {
	matches, err := filepath.Glob(filepath.Join(s.dir, Stream+"-*.jsonl"))
	if err != nil || len(matches) <= s.maxFiles {
		return
	}
	sort.Slice(matches, func(i, j int) bool {
		left, leftErr := os.Stat(matches[i])
		right, rightErr := os.Stat(matches[j])
		if leftErr != nil || rightErr != nil || left.ModTime().Equal(right.ModTime()) {
			return matches[i] < matches[j]
		}
		return left.ModTime().Before(right.ModTime())
	})
	for _, path := range matches[:len(matches)-s.maxFiles] {
		os.Remove(path)
	}
}
