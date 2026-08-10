package parsers

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// sessionMetadata is the structured JSON Claude Desktop and Cowork write next to
// a session. It is not a transcript: it carries no exchange, only what the
// runtime knows about the conversation.
type sessionMetadata struct {
	CliSessionID   string          `json:"cliSessionId"`
	SessionID      string          `json:"sessionId"`
	Cwd            string          `json:"cwd"`
	OriginCwd      string          `json:"originCwd"`
	Title          string          `json:"title"`
	CreatedAt      *float64        `json:"createdAt"`
	LastActivityAt *float64        `json:"lastActivityAt"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permissionMode"`
	InitialMessage string          `json:"initialMessage"`
	ProcessName    string          `json:"processName"`
	VMProcessName  string          `json:"vmProcessName"`
	Folders        json.RawMessage `json:"userSelectedFolders"`
	MCPTools       json.RawMessage `json:"enabledMcpTools"`
}

// sidecarView is the little the Cowork audit path needs from its paired file.
type sidecarView struct {
	sessionID      string
	title          string
	initialMessage string
}

func readSessionMetadata(content []byte) sidecarView {
	if len(content) == 0 {
		return sidecarView{}
	}
	var payload sessionMetadata
	if err := json.Unmarshal(content, &payload); err != nil {
		return sidecarView{}
	}
	return sidecarView{
		sessionID:      firstNonEmpty(payload.CliSessionID, payload.SessionID),
		title:          strings.TrimSpace(payload.Title),
		initialMessage: payload.InitialMessage,
	}
}

// ParseSessionMetadata turns a Claude Desktop or Cowork metadata file into a
// session snapshot: no exchange, and every field it does know merged over
// whatever the transcript already wrote.
func ParseSessionMetadata(content []byte, meta FileMeta) (Records, error) {
	var payload sessionMetadata
	if err := json.Unmarshal(content, &payload); err != nil {
		// A file that is not the metadata document is not a failure of the
		// ingest: the directory it lives in holds other things.
		return Records{}, nil
	}
	sessionID := firstNonEmpty(payload.CliSessionID, payload.SessionID)
	if sessionID == "" {
		return Records{}, nil
	}

	sourceAgent := firstNonEmpty(meta.SourceAgent, "claude-code")
	entrypoint := "claude-desktop"
	if sourceAgent == "claude-cowork" {
		entrypoint = "claude-cowork"
	}

	session := Session{
		ID:          sessionID,
		SourceAgent: sourceAgent,
		Project:     meta.Project,
		StartedAt:   isoFromEpochMS(payload.CreatedAt),
		EndedAt:     isoFromEpochMS(payload.LastActivityAt),
		Title:       strings.TrimSpace(payload.Title),
		Snapshot:    true,
		Metadata: WithoutEmpty(map[string]any{
			"entrypoint":            entrypoint,
			"local_session_id":      payload.SessionID,
			"cwd":                   firstNonEmpty(payload.Cwd, payload.OriginCwd),
			"model":                 payload.Model,
			"permission_mode":       payload.PermissionMode,
			"initial_message":       payload.InitialMessage,
			"created_at_ms":         payload.CreatedAt,
			"last_activity_at_ms":   payload.LastActivityAt,
			"process_name":          payload.ProcessName,
			"vm_process_name":       payload.VMProcessName,
			"user_selected_folders": rawJSON(payload.Folders),
			"enabled_mcp_tools":     rawJSON(payload.MCPTools),
		}),
	}
	return Records{Sessions: []Session{session}}, nil
}

// isoLayout is the one instant format the whole corpus is written in. Mixing two
// of them in one column makes every comparison between sources a lie.
const isoLayout = "2006-01-02T15:04:05.999999999"

// ISOFromEpochMS turns the millisecond epoch the runtimes write into UTC ISO 8601.
func ISOFromEpochMS(value float64) string {
	return time.UnixMilli(int64(value)).UTC().Format(isoLayout) + "Z"
}

// ISOFromEpochSeconds is the same for a source counting in seconds.
func ISOFromEpochSeconds(value float64) string { return ISOFromEpochMS(value * 1000) }

// isoFromEpochMS is the same for a field the document may not carry at all.
func isoFromEpochMS(value *float64) string {
	if value == nil {
		return ""
	}
	return ISOFromEpochMS(*value)
}

// isoFromAnyInstant reads whichever of the three shapes an agent wrote: an ISO
// string, a millisecond epoch as a number, or that same epoch as a string.
func isoFromAnyInstant(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		if typed == "" {
			return ""
		}
		if digits, err := strconv.ParseFloat(typed, 64); err == nil {
			return isoFromEpochMS(&digits)
		}
		if parsed, ok := parseISO(typed); ok {
			return parsed.UTC().Format(isoLayout) + "Z"
		}
		return ""
	case float64:
		return isoFromEpochMS(&typed)
	}
	return ""
}

// rawJSON keeps a structured field as the text the file wrote. The metadata
// column is a JSON document, so a list stays a list.
func rawJSON(value json.RawMessage) any {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil
	}
	return decoded
}

// WithoutEmpty drops the keys with nothing in them, so a snapshot never patches
// a known value with a blank one. The source adapters that read a live database
// build their metadata the same way.
func WithoutEmpty(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		switch typed := value.(type) {
		case nil:
		case string:
			if typed != "" {
				out[key] = typed
			}
		case *float64:
			if typed != nil {
				out[key] = *typed
			}
		default:
			out[key] = value
		}
	}
	return out
}
