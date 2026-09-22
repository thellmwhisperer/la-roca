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
// database. EventSHA makes repeated ingest idempotent while retaining repeated
// fires, including identical lines.
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
	lineReader := bufio.NewReaderSize(reader, 64*1024)
	var events []Event
	occurrences := make(map[string]int)
	line := 0
	for {
		rawLine, readErr := lineReader.ReadString('\n')
		if len(rawLine) > maxLineSize {
			return nil, fmt.Errorf("read recall log line %d: exceeds %d bytes", line+1, maxLineSize)
		}
		if len(rawLine) == 0 && readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("read recall log: %w", readErr)
		}
		line++
		raw := strings.TrimSpace(rawLine)
		if raw == "" {
			if readErr == io.EOF {
				break
			}
			continue
		}
		var document map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			if readErr == io.EOF && strings.Contains(err.Error(), "unexpected end of JSON input") {
				break
			}
			return nil, fmt.Errorf("recall log line %d: %w", line, err)
		}
		event, err := decode(document)
		if err != nil {
			return nil, fmt.Errorf("recall log line %d: %w", line, err)
		}
		digest := sha256.Sum256([]byte(raw))
		baseSHA := hex.EncodeToString(digest[:])
		occurrence := occurrences[baseSHA]
		occurrences[baseSHA] = occurrence + 1
		event.EventSHA = baseSHA
		if occurrence > 0 {
			digest = sha256.Sum256([]byte(raw + "\x00" + strconv.Itoa(occurrence)))
			event.EventSHA = hex.EncodeToString(digest[:])
		}
		events = append(events, event)
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return nil, fmt.Errorf("read recall log: %w", readErr)
		}
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
	if err := optionalField(document, "query", &event.Query); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "session_id", &event.SessionID); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "exchange_id", &event.ExchangeID); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "raw", &event.Raw); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "hits", &event.Hits); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "top_score", &event.TopScore); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "ids", &event.IDs); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "scores", &event.Scores); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "dates", &event.Dates); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "cands", &event.Cands); err != nil {
		return Event{}, err
	}
	if err := optionalField(document, "elapsed_ms", &event.ElapsedMS); err != nil {
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

func optionalField[T any](document map[string]json.RawMessage, name string, target *T) error {
	raw, ok := document[name]
	if !ok || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
