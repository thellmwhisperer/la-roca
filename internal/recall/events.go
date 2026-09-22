// Package recall decodes the durable provenance records written by the recall hook.
package recall

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const maxLineSize = 4 << 20

// Event is one recall hook fire. Query and session provenance are optional so
// older logs remain importable; new producers should fill both fields.
type Event struct {
	TS         string
	Action     string
	Tool       string
	QuerySHA   string
	Query      string
	SessionID  string
	ExchangeID *int64
	Raw        *int64
	Hits       *int64
	TopScore   *float64
	IDs        []string
	Scores     []float64
	Dates      []string
	Cands      *int64
	ElapsedMS  *int64
	EventSHA   string
}

// ReadFile reads a recall JSONL log without retaining its source path in the
// database. EventSHA makes repeated ingest idempotent while keeping duplicate
// fires with different timestamps distinct.
func ReadFile(path string) ([]Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return Read(file)
}

// Read decodes one JSON object per line from a recall log.
func Read(reader io.Reader) ([]Event, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxLineSize)
	var events []Event
	line := 0
	for scanner.Scan() {
		line++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			return nil, fmt.Errorf("recall log line %d: %w", line, err)
		}
		event, err := decode(document)
		if err != nil {
			return nil, fmt.Errorf("recall log line %d: %w", line, err)
		}
		digest := sha256.Sum256([]byte(raw))
		event.EventSHA = hex.EncodeToString(digest[:])
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read recall log: %w", err)
	}
	return events, nil
}

func decode(document map[string]json.RawMessage) (Event, error) {
	var event Event
	if err := stringField(document, "ts", &event.TS); err != nil {
		return Event{}, err
	}
	if err := stringField(document, "action", &event.Action); err != nil {
		return Event{}, err
	}
	if err := stringField(document, "query_sha", &event.QuerySHA); err != nil {
		return Event{}, err
	}
	_ = stringField(document, "tool", &event.Tool)
	if err := firstStringField(document, &event.Query, "query", "stimulus"); err != nil {
		return Event{}, err
	}
	_ = firstStringField(document, &event.SessionID, "session_id", "session")
	if err := intField(document, "exchange_id", &event.ExchangeID); err != nil {
		return Event{}, err
	}
	if err := intField(document, "raw", &event.Raw); err != nil {
		return Event{}, err
	}
	if err := intField(document, "hits", &event.Hits); err != nil {
		return Event{}, err
	}
	if err := floatField(document, "top_score", &event.TopScore); err != nil {
		return Event{}, err
	}
	if err := stringSliceField(document, "ids", &event.IDs); err != nil {
		return Event{}, err
	}
	if err := floatSliceField(document, "scores", &event.Scores); err != nil {
		return Event{}, err
	}
	if err := stringSliceField(document, "dates", &event.Dates); err != nil {
		return Event{}, err
	}
	if err := intField(document, "cands", &event.Cands); err != nil {
		return Event{}, err
	}
	if err := intField(document, "elapsed_ms", &event.ElapsedMS); err != nil {
		return Event{}, err
	}
	return event, nil
}

func stringField(document map[string]json.RawMessage, name string, target *string) error {
	raw, ok := document[name]
	if !ok {
		return fmt.Errorf("missing %q", name)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func firstStringField(document map[string]json.RawMessage, target *string, names ...string) error {
	for _, name := range names {
		if raw, ok := document[name]; ok {
			if err := json.Unmarshal(raw, target); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			return nil
		}
	}
	return nil
}

func intField(document map[string]json.RawMessage, name string, target **int64) error {
	raw, ok := document[name]
	if !ok || string(raw) == "null" {
		return nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		var text string
		if textErr := json.Unmarshal(raw, &text); textErr != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		parsed, parseErr := strconv.ParseInt(text, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("%s: %w", name, parseErr)
		}
		value = parsed
	}
	*target = &value
	return nil
}

func floatField(document map[string]json.RawMessage, name string, target **float64) error {
	raw, ok := document[name]
	if !ok || string(raw) == "null" {
		return nil
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	*target = &value
	return nil
}

func stringSliceField(document map[string]json.RawMessage, name string, target *[]string) error {
	raw, ok := document[name]
	if !ok || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func floatSliceField(document map[string]json.RawMessage, name string, target *[]float64) error {
	raw, ok := document[name]
	if !ok || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
