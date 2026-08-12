package logfile

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type QueryFailure struct {
	Timestamp     time.Time `json:"timestamp"`
	Source        string    `json:"source"`
	Operation     string    `json:"operation"`
	Question      string    `json:"question,omitempty"`
	Error         string    `json:"error"`
	ErrorType     string    `json:"error_type"`
	CorrelationID string    `json:"correlation_id,omitempty"`
}

type QueryFailureSummary struct {
	Since     time.Time      `json:"since"`
	Count     int            `json:"count"`
	Recent    []QueryFailure `json:"recent"`
	Malformed int            `json:"malformed_lines,omitempty"`
}

type queryFailureRecord struct {
	Timestamp     time.Time `json:"timestamp"`
	Source        string    `json:"source"`
	Command       string    `json:"command"`
	Tool          string    `json:"tool"`
	OK            *bool     `json:"ok"`
	ExitCode      int       `json:"exit_code"`
	Error         string    `json:"error"`
	ErrorType     string    `json:"error_type"`
	CorrelationID string    `json:"correlation_id"`
	Question      string    `json:"question"`
	Fallback      string    `json:"fallback_reason"`
	Degraded      string    `json:"degraded"`
	Result        struct {
		Question      string `json:"question"`
		Message       string `json:"message"`
		ProviderError string `json:"provider_error"`
		Degraded      string `json:"degraded"`
	} `json:"result"`
}

func (w *Writer) RecentQueryFailures(now time.Time, window time.Duration,
	limit int) (QueryFailureSummary, error) {
	summary := QueryFailureSummary{Since: now.UTC().Add(-window)}
	for _, stream := range []string{Executions, MCPAudit} {
		paths, err := filepath.Glob(filepath.Join(w.dir, stream+"-*.jsonl"))
		if err != nil {
			return summary, err
		}
		for _, path := range paths {
			if err := readQueryFailures(path, summary.Since, &summary); err != nil {
				return summary, err
			}
		}
	}
	sort.Slice(summary.Recent, func(i, j int) bool {
		return summary.Recent[i].Timestamp.After(summary.Recent[j].Timestamp)
	})
	summary.Count = len(summary.Recent)
	if limit >= 0 && len(summary.Recent) > limit {
		summary.Recent = summary.Recent[:limit]
	}
	return summary, nil
}

func readQueryFailures(path string, since time.Time, summary *QueryFailureSummary) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), int(maxFileBytes)+1)
	for scanner.Scan() {
		var record queryFailureRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			summary.Malformed++
			continue
		}
		failure, ok := record.queryFailure(since)
		if ok {
			summary.Recent = append(summary.Recent, failure)
		}
	}
	return scanner.Err()
}

func (r queryFailureRecord) queryFailure(since time.Time) (QueryFailure, bool) {
	operation := r.Command
	source := r.Source
	queryCall := operation == "query"
	if r.Source == "mcp" || operation == "" {
		operation = r.Tool
		queryCall = operation == "roca_query" || operation == "roca_sql"
		if source == "" {
			source = "mcp"
		}
	} else if source == "" {
		source = "cli"
	}
	failed := r.ExitCode != 0
	if r.OK != nil {
		failed = !*r.OK
	}
	if !queryCall || !failed || r.Timestamp.Before(since) {
		return QueryFailure{}, false
	}
	errorText := firstNonEmpty(r.Error, r.Result.ProviderError, r.Result.Message,
		r.Fallback, r.Degraded, r.Result.Degraded, "query failed")
	errorType := firstNonEmpty(r.ErrorType, r.Fallback, r.Degraded,
		r.Result.Degraded, "query_error")
	return QueryFailure{
		Timestamp: r.Timestamp, Source: source, Operation: operation,
		Question: firstNonEmpty(r.Question, r.Result.Question), Error: errorText,
		ErrorType: errorType, CorrelationID: r.CorrelationID,
	}, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
