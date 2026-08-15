package parsers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Destination says which public surface owns a parser's normalized records.
// Source syntax is deliberately absent: JSON, JSONL, Markdown and future
// encodings are private implementation details of Parser.
type Destination uint8

const (
	DestinationCorpus Destination = 1 << iota
	DestinationStore
	DestinationBoth = DestinationCorpus | DestinationStore
)

func (d Destination) String() string {
	switch d {
	case DestinationCorpus:
		return "corpus"
	case DestinationStore:
		return "store"
	case DestinationBoth:
		return "both"
	default:
		return "invalid"
	}
}

// Conforms checks that normalized records can only reach the destination the
// parser declared. Empty results and discards are valid for every destination.
func (d Destination) Conforms(records Records) error {
	if d == 0 || d&^DestinationBoth != 0 {
		return fmt.Errorf("parser declares invalid destination %d", d)
	}
	if d&DestinationCorpus == 0 && len(records.Sessions) > 0 {
		return fmt.Errorf("%s parser produced corpus sessions", d.String())
	}
	if d&DestinationStore == 0 && len(records.Memories) > 0 {
		return fmt.Errorf("%s parser produced store memories", d.String())
	}
	return nil
}

// File is the complete, deterministic input to a parser. Detection and parsing
// receive the same bytes and scan metadata, so neither capability needs a
// database, a clock or hidden process state.
type File struct {
	Content []byte
	Meta    FileMeta
}

// Parser is the contribution contract. A parser has exactly two capabilities:
// claim files that belong to its agent and normalize a claimed file.
type Parser interface {
	Detect(File) bool
	Parse(File) (Records, error)
}

// Registration binds a parser to its stable ingest identity and destination.
// Name is not a file format; a parser may recognize any encoding internally.
type Registration struct {
	Name        string
	SourceAgent string
	// Locations are session-store directories relative to the operator's home,
	// or absolute paths when an agent has a platform-independent location. The
	// ingest scanner walks only these declared roots; unchanged fingerprints are
	// still skipped before Detect sees candidate bytes. Established adapters with
	// specialized scanners leave it empty.
	Locations []string
	// HarvestLocations opt an established, source-specific scanner into the
	// present-agent smoke test. Contributions already use Locations for both
	// ingest and smoke; this separate list lets an established scanner prove its
	// real surface without also entering the generic scan route.
	HarvestLocations []string
	// Version is the reading this parser currently gives its source. It rides
	// inside the ingest watermark, so bumping it is what makes a build that
	// learned to read more re-read the files it already synced. An empty version
	// is a reading that has never changed, and its files stay skipped.
	Version     string
	Destination Destination
	Parser      Parser
}

// ResolveLocations turns the declared locations into absolute roots and reports
// the ones it refuses. A location must name a store inside the tree it declares:
// an empty entry, a bare home, and one that climbs out of the home directory all
// resolve to somewhere far wider than any agent's session store, and a scan that
// wide would fingerprint the operator's whole machine.
func (r Registration) ResolveLocations(home string) (roots, refused []string) {
	return resolveLocations(home, r.Locations)
}

// ResolveHarvestLocations returns the narrow roots the present-agent smoke
// reads. A contributed parser's declared ingest locations are its smoke roots;
// an established parser can declare HarvestLocations without changing scans.
func (r Registration) ResolveHarvestLocations(home string) (roots, refused []string) {
	if len(r.HarvestLocations) == 0 {
		return r.ResolveLocations(home)
	}
	return resolveLocations(home, r.HarvestLocations)
}

func resolveLocations(home string, declared []string) (roots, refused []string) {
	for _, location := range declared {
		root, usable := resolveLocation(home, location)
		if !usable {
			refused = append(refused, location)
			continue
		}
		roots = append(roots, root)
	}
	return roots, refused
}

func resolveLocation(home, declared string) (string, bool) {
	if strings.TrimSpace(declared) == "" {
		return "", false
	}
	cleaned := filepath.Clean(declared)
	if filepath.IsAbs(cleaned) {
		return cleaned, filepath.Dir(cleaned) != cleaned
	}
	if home == "" || cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(home, cleaned), true
}

// Parse normalizes one file and enforces the declared routing boundary before
// records can reach a writer.
func (r Registration) Parse(file File) (Records, error) {
	if !r.Parser.Detect(file) {
		return Records{Discards: []Discard{
			Excluded("file is not claimed by the registered parser"),
		}}, nil
	}
	return r.parseClaimed(file)
}

func (r Registration) parseClaimed(file File) (Records, error) {
	records, err := r.Parser.Parse(file)
	if err != nil {
		return Records{}, err
	}
	if err := r.Destination.Conforms(records); err != nil {
		return Records{}, fmt.Errorf("parser %q: %w", r.Name, err)
	}
	return records, nil
}

type parserFunctions struct {
	detect func(File) bool
	parse  func([]byte, FileMeta) (Records, error)
}

func (p parserFunctions) Detect(file File) bool { return p.detect(file) }
func (p parserFunctions) Parse(file File) (Records, error) {
	return p.parse(file.Content, file.Meta)
}

// registry is the single contribution point. The identifier preserves ingest
// fingerprints and reports; the parser owns every detail of its source syntax.
var registry = []Registration{
	fileParser(KindClaudeSession, DestinationCorpus, detectClaudeSession, ParseClaudeSession),
	fileParser(KindClaudeMemory, DestinationStore, detectClaudeMemory, ParseClaudeMemory),
	fileParser(KindSessionMetadata, DestinationCorpus, detectSessionMetadata, ParseSessionMetadata),
	fileParser(KindCoworkAudit, DestinationCorpus, detectCoworkAudit, ParseCoworkAudit),
	fileParser(KindCodexSession, DestinationCorpus, detectCodexSession, ParseCodexSession),
	fileParser(KindCodexHistory, DestinationCorpus, detectCodexHistory, func(content []byte, meta FileMeta) (Records, error) {
		return parseCodexHistory(content, meta), nil
	}),
	fileParser(KindCodexFile, DestinationStore, detectCodexFile, ParseCodexFile),
	fileParser(KindCodexMemoryAggregate, DestinationStore, detectCodexMemoryAggregate, ParseCodexMemoryAggregate),
	fileParser(KindSubagent, DestinationCorpus, detectSubagent, ParseSubagent),
	fileParser(KindPiSession, DestinationCorpus, detectPiSession, ParsePiSession),
	fileParser(KindClaudeWebConversations, DestinationCorpus, detectClaudeWebConversations,
		func(content []byte, meta FileMeta) (Records, error) {
			return ParseClaudeWebConversations(bytes.NewReader(content), meta)
		}),
	fileParser(KindClaudeWebMemories, DestinationStore, detectClaudeWebMemories,
		func(content []byte, meta FileMeta) (Records, error) {
			return ParseClaudeWebMemories(bytes.NewReader(content), meta)
		}),
	fileParser(KindChatGPTWebConversations, DestinationCorpus, detectChatGPTWebConversations,
		func(content []byte, meta FileMeta) (Records, error) {
			return ParseChatGPTWebConversations(bytes.NewReader(content), meta)
		}),
	{
		Name: string(KindGrokSession), SourceAgent: "grok",
		HarvestLocations: []string{".grok/sessions"}, Version: "grok-session-v2",
		Destination: DestinationCorpus,
		Parser:      parserFunctions{detect: detectGrokSession, parse: ParseGrokSession},
	},
	fileParser(KindGrokSessionMetadata, DestinationCorpus, detectGrokSessionMetadata,
		ParseGrokSessionMetadata),
}

func fileParser(kind Kind, destination Destination, detect func(File) bool,
	parse func([]byte, FileMeta) (Records, error)) Registration {
	return Registration{Name: string(kind), Destination: destination,
		Parser: parserFunctions{detect: detect, parse: parse}}
}

// Registered returns a copy so tests and contributor tooling can inspect the
// catalogue without changing process-global routing.
func Registered() []Registration { return append([]Registration(nil), registry...) }

// Lookup finds the one registry line for a parser identifier.
func Lookup(name string) (Registration, bool) {
	for _, registered := range registry {
		if registered.Name == name {
			return registered, true
		}
	}
	return Registration{}, false
}

// Detect asks a registered parser whether the file belongs to it.
func Detect(kind Kind, content []byte, meta FileMeta) bool {
	registered, ok := Lookup(string(kind))
	return ok && registered.Parser.Detect(File{Content: content, Meta: meta})
}

// Conform enforces destination routing for streaming parsers that normalize an
// io.Reader directly instead of going through Parse's byte-slice adapter.
func Conform(kind Kind, records Records) error {
	registered, ok := Lookup(string(kind))
	if !ok {
		return fmt.Errorf("there is no parser for source kind %q", kind)
	}
	if err := registered.Destination.Conforms(records); err != nil {
		return fmt.Errorf("parser %q: %w", registered.Name, err)
	}
	return nil
}

func sourceIs(meta FileMeta, allowed ...string) bool {
	if meta.SourceAgent == "" {
		return true
	}
	for _, source := range allowed {
		if meta.SourceAgent == source {
			return true
		}
	}
	return false
}

func firstObject(content []byte) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(content, &object) == nil && object != nil {
		return object
	}

	decoder := json.NewDecoder(bytes.NewReader(content))
	token, err := decoder.Token()
	if err != nil {
		return nil
	}
	if token == json.Delim('[') {
		if !decoder.More() {
			return nil
		}
		var object map[string]json.RawMessage
		if err := decoder.Decode(&object); err == nil {
			return object
		}
		return nil
	}
	for _, line := range lines(content) {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &object) == nil {
			return object
		}
	}
	return nil
}

func stringField(object map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(object[key], &value)
	return value
}

func has(object map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}

func detectClaudeSession(file File) bool {
	if !sourceIs(file.Meta, "claude") {
		return false
	}
	object := firstObject(file.Content)
	kind := stringField(object, "type")
	return (kind == "user" || kind == "assistant" || kind == "summary") &&
		!has(object, "agentId", "sessionId", "_audit_timestamp")
}

func detectClaudeMemory(file File) bool {
	return sourceIs(file.Meta, "claude") && firstObject(file.Content) == nil &&
		strings.TrimSpace(string(file.Content)) != ""
}

func detectSessionMetadata(file File) bool {
	if !sourceIs(file.Meta, "claude-desktop", "cowork", "claude-cowork") {
		return false
	}
	return has(firstObject(file.Content), "cliSessionId", "sessionId")
}

func detectCoworkAudit(file File) bool {
	if !sourceIs(file.Meta, "cowork", "claude-cowork") {
		return false
	}
	object := firstObject(file.Content)
	return has(object, "_audit_timestamp", "session_id") && has(object, "message")
}

func detectCodexSession(file File) bool {
	if !sourceIs(file.Meta, "codex") {
		return false
	}
	kind := stringField(firstObject(file.Content), "type")
	return kind == "session_meta" || kind == "turn_context" || kind == "event_msg" ||
		kind == "response_item"
}

func detectCodexHistory(file File) bool {
	if !sourceIs(file.Meta, "codex") {
		return false
	}
	object := firstObject(file.Content)
	return stringField(object, "type") == "" && has(object, "session_id", "text", "ts")
}

func detectCodexFile(file File) bool {
	return sourceIs(file.Meta, "codex") &&
		(file.Meta.SourceType == "memory" || file.Meta.SourceType == "rule") &&
		strings.TrimSpace(string(file.Content)) != ""
}

func detectCodexMemoryAggregate(file File) bool {
	return sourceIs(file.Meta, "codex") && codexThreadBoundary.Match(file.Content)
}

func detectSubagent(file File) bool {
	if !sourceIs(file.Meta, "claude", "claude-code") {
		return false
	}
	confirmed, _ := LooksLikeSubagent(file.Content)
	return confirmed
}

func detectPiSession(file File) bool {
	if !sourceIs(file.Meta, "pi") {
		return false
	}
	object := firstObject(file.Content)
	return stringField(object, "type") == "session" && has(object, "version", "id", "cwd")
}

func detectClaudeWebConversations(file File) bool {
	return sourceIs(file.Meta, "claude-web") && has(firstObject(file.Content), "chat_messages")
}

func detectClaudeWebMemories(file File) bool {
	return sourceIs(file.Meta, "claude-web") && has(firstObject(file.Content), "memory")
}

func detectChatGPTWebConversations(file File) bool {
	return sourceIs(file.Meta, "chatgpt-web") && has(firstObject(file.Content), "mapping", "conversation_id")
}

func detectGrokSession(file File) bool {
	if !sourceIs(file.Meta, "grok") {
		return false
	}
	var line grokUpdateLine
	if object := firstObject(file.Content); object == nil {
		return false
	} else if raw, err := json.Marshal(object); err != nil || json.Unmarshal(raw, &line) != nil {
		return false
	}
	return (line.Method == "session/update" || line.Method == "_x.ai/session/update") &&
		line.Params.SessionID != "" && line.Params.Update.SessionUpdate != ""
}

func detectGrokSessionMetadata(file File) bool {
	if !sourceIs(file.Meta, "grok") {
		return false
	}
	object := firstObject(file.Content)
	return has(object, "info") && has(object, "chat_format_version", "session_summary", "created_at")
}
