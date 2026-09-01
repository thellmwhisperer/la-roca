package agentcfg

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// The JSON and JSONC editor.
//
// It never decodes the document and re-encodes it: that would be four lines and
// it would silently eat the operator's comments, their key order and their
// formatting. Instead it locates the byte range of exactly one member and
// rewrites those bytes.
//
// JSONC comments are blanked into a same-length *view*, so every offset found in the
// view is the same offset in the real text. The edits then apply to the real
// bytes and the comments never move.

// member is one key/value pair of an object and where it sits in the text.
type member struct {
	key string
	// start is the first byte of the key's opening quote, valueStart the first
	// byte of the value, and end the byte just past the value.
	start, valueStart, end int
}

const (
	ZcodeMCPPreimageNone       = "none"
	ZcodeMCPPreimageServers    = "servers"
	ZcodeMCPPreimageMCPServers = "mcp+servers"
)

func ZcodeMCPPreimage(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return ZcodeMCPPreimageMCPServers, nil
	}
	r := runtime{kind: kindJSON}
	view, root, err := rootObject(r, text)
	if err != nil {
		return "", err
	}
	mcpIndex := root.find("mcp")
	if mcpIndex < 0 {
		return ZcodeMCPPreimageMCPServers, nil
	}
	mcp, err := objectAt(view, root.members[mcpIndex].valueStart)
	if err != nil {
		return "", fmt.Errorf("mcp must be an object: %w", err)
	}
	serversIndex := mcp.find("servers")
	if serversIndex < 0 {
		return ZcodeMCPPreimageServers, nil
	}
	servers, err := objectAt(view, mcp.members[serversIndex].valueStart)
	if err != nil {
		return "", fmt.Errorf("servers must be an object: %w", err)
	}
	if servers.find(ServerName) >= 0 {
		return ZcodeMCPPreimageNone, nil
	}
	return ZcodeMCPPreimageNone, nil
}

func withdrawZcodeMCP(r runtime, text, preimage string) (string, error) {
	next, err := jsonRemove(r, text, []string{ServerName})
	if err != nil || preimage == ZcodeMCPPreimageNone {
		return next, err
	}
	if preimage == ZcodeMCPPreimageServers || preimage == ZcodeMCPPreimageMCPServers {
		next, err = cutJSONMemberIfEmpty(r, next, []string{"mcp"}, "servers")
		if err != nil {
			return "", err
		}
	}
	if preimage == ZcodeMCPPreimageMCPServers {
		next, err = cutJSONMemberIfEmpty(r, next, nil, "mcp")
	}
	return next, err
}

func cutJSONMemberIfEmpty(r runtime, text string, path []string, key string) (string, error) {
	return cutJSONMember(r, text, path, key, func(view string, valueStart int) (bool, error) {
		inside, err := objectAt(view, valueStart)
		return err == nil && len(inside.members) == 0, err
	})
}

func jsonDeclare(r runtime, text string, entry fields) (string, error) {
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	view, root, err := rootObject(r, text)
	if err != nil {
		return "", err
	}

	container := root
	for i, parent := range r.parents {
		memberIndex := container.find(parent)
		if memberIndex < 0 {
			pad := padUnder(view, container, indentOf(view, container.close)+indent)
			keys := append(append([]string{}, r.parents[i:]...), r.serversKey)
			return container.insert(text, renderJSONPath(keys, entry, pad),
				indentOf(view, container.close)), nil
		}
		next, err := objectAt(view, container.members[memberIndex].valueStart)
		if err != nil {
			return "", fmt.Errorf("%s must be an object: %w", parent, err)
		}
		container = next
	}

	inside, servers, ok, err := serversInside(r, view, container)
	if err != nil {
		return "", err
	}
	if !ok {
		// This runtime has never declared a server. The whole map goes in as one
		// new member of the root object.
		pad := padUnder(view, container, indentOf(view, container.close)+indent)
		return container.insert(text, renderJSONPath([]string{r.serversKey}, entry, pad),
			indentOf(view, container.close)), nil
	}

	pad := padUnder(view, inside, indentOf(view, servers.start)+indent)
	rendered := renderJSON(entry, pad)
	if i := inside.find(ServerName); i >= 0 {
		// Replaced in place, so a comment sitting above it stays above it.
		existing := inside.members[i]
		return text[:existing.valueStart] + rendered + text[existing.end:], nil
	}
	return inside.insert(text, pad+quote(ServerName)+": "+rendered,
		indentOf(view, servers.start)), nil
}

func jsonRemove(r runtime, text string, entries []string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	view, root, err := rootObject(r, text)
	if err != nil {
		return "", err
	}
	container, ok, err := objectAtPath(view, root, r.parents)
	if err != nil || !ok {
		return text, err
	}
	inside, _, ok, err := serversInside(r, view, container)
	if err != nil || !ok {
		return text, err
	}
	// Backwards, so that removing one member does not move the offsets of the
	// ones still to be looked at.
	var removed int
	for i := len(inside.members) - 1; i >= 0; i-- {
		if slices.Contains(entries, inside.members[i].key) {
			text = inside.cut(text, i)
			removed++
		}
	}
	// When this operation empties a flat servers object, remove the key. This
	// restores files where install created the map, but without persisted
	// container provenance it also removes a pre-existing empty map; the public
	// contract and follow-up ownership work are documented in docs/mcp.md.
	if removed > 0 && removed == len(inside.members) && len(r.parents) == 0 {
		text, err = cutJSONMemberAtPath(r, text, r.parents, r.serversKey)
		if err != nil {
			return "", err
		}
	}
	return text, nil
}

func renderJSONPath(keys []string, entry fields, pad string) string {
	if len(keys) == 0 {
		return pad + quote(ServerName) + ": " + renderJSON(entry, pad)
	}
	childPad := pad + indent
	return pad + quote(keys[0]) + ": {\n" +
		renderJSONPath(keys[1:], entry, childPad) + "\n" + pad + "}"
}

func objectAtPath(view string, root object, path []string) (object, bool, error) {
	current := root
	for _, key := range path {
		i := current.find(key)
		if i < 0 {
			return object{}, false, nil
		}
		next, err := objectAt(view, current.members[i].valueStart)
		if err != nil {
			return object{}, false, fmt.Errorf("%s must be an object: %w", key, err)
		}
		current = next
	}
	return current, true, nil
}

func cutJSONMemberAtPath(r runtime, text string, path []string, key string) (string, error) {
	return cutJSONMember(r, text, path, key, nil)
}

func cutJSONMember(r runtime, text string, path []string, key string,
	removable func(string, int) (bool, error)) (string, error) {
	view, root, err := rootObject(r, text)
	if err != nil {
		return "", err
	}
	container, ok, err := objectAtPath(view, root, path)
	if err != nil || !ok {
		return text, err
	}
	index := container.find(key)
	if index < 0 {
		return text, nil
	}
	if removable != nil {
		ok, err = removable(view, container.members[index].valueStart)
		if err != nil || !ok {
			return text, err
		}
	}
	return container.cut(text, index), nil
}

// jsonDecode reads the view and not the text: a JSONC comment would not survive
// a JSON decoder, and the view has none. The document is the one jsonView
// already proved is a single object, so there is no second parse.
func jsonDecode(r runtime, text string) (map[string]any, error) {
	_, document, err := jsonView(r, text)
	return document, err
}

// serversInside is the object at serversKey, or ok=false when the key is absent.
func serversInside(r runtime, view string, container object) (object, member, bool, error) {
	key := container.find(r.serversKey)
	if key < 0 {
		return object{}, member{}, false, nil
	}
	servers := container.members[key]
	inside, err := objectAt(view, servers.valueStart)
	if err != nil {
		return object{}, member{}, false, fmt.Errorf("%s must be an object: %w", r.serversKey, err)
	}
	return inside, servers, true, nil
}

func DeclareZcodeSessionStartHook(text, marker, command string, timeoutMs int) (string, error) {
	if marker == "" {
		return "", fmt.Errorf("ZCode SessionStart marker must not be empty")
	}
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	view, root, err := rootObject(runtime{kind: kindJSON}, text)
	if err != nil {
		return "", err
	}
	preimage := zcodeHookPreimage{}
	container := root
	path := []string{"hooks", "events"}
	for i, key := range path {
		memberIndex := container.find(key)
		if memberIndex < 0 {
			preimage.Hooks = i == 0
			preimage.Events = true
			preimage.SessionStart = true
			owned := zcodeOwnedHookGroup(marker, preimage, command, timeoutMs)
			pad := padUnder(view, container, indentOf(view, container.close)+indent)
			keys := append(append([]string{}, path[i:]...), "SessionStart")
			return container.insert(text,
				renderJSONValuePath(keys, []zcodeHookGroup{owned}, pad),
				indentOf(view, container.close)), nil
		}
		next, err := objectAt(view, container.members[memberIndex].valueStart)
		if err != nil {
			return "", fmt.Errorf("%s must be an object: %w", key, err)
		}
		container = next
	}
	sessionIndex := container.find("SessionStart")
	if sessionIndex < 0 {
		preimage.SessionStart = true
		owned := zcodeOwnedHookGroup(marker, preimage, command, timeoutMs)
		pad := padUnder(view, container, indentOf(view, container.close)+indent)
		return container.insert(text, renderJSONValuePath([]string{"SessionStart"},
			[]zcodeHookGroup{owned}, pad), indentOf(view, container.close)), nil
	}
	entries, err := arrayAt(view, container.members[sessionIndex].valueStart)
	if err != nil {
		return "", fmt.Errorf("SessionStart must be an array: %w", err)
	}
	for _, entry := range entries.values {
		group, err := objectAt(view, entry.start)
		if err != nil {
			continue
		}
		recorded, owned := parseZcodeHookMarker(marker, jsonStringMember(view, group, "matcher"))
		if !owned {
			continue
		}
		managed := zcodeOwnedHookGroup(marker, recorded, command, timeoutMs)
		pad := indentOf(view, entry.start)
		rendered, err := json.MarshalIndent(managed, pad, indent)
		if err != nil {
			return "", err
		}
		return text[:entry.start] + string(rendered) + text[entry.end:], nil
	}
	owned := zcodeOwnedHookGroup(marker, preimage, command, timeoutMs)
	pad := arrayPadUnder(view, entries,
		indentOf(view, container.members[sessionIndex].start)+indent)
	rendered, err := json.MarshalIndent(owned, pad, indent)
	if err != nil {
		return "", err
	}
	return entries.insert(text, pad+string(rendered), indentOf(view, entries.close)), nil
}

func ZcodeHooksEnabled(text string) (bool, bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, false, nil
	}
	document, err := jsonDecode(runtime{kind: kindJSON}, text)
	if err != nil {
		return false, false, err
	}
	hooks, present := document["hooks"].(map[string]any)
	if !present {
		return false, false, nil
	}
	value, declared := hooks["enabled"]
	if !declared {
		return false, false, nil
	}
	enabled, valid := value.(bool)
	if !valid {
		return true, false, fmt.Errorf("hooks.enabled must be a boolean")
	}
	return true, enabled, nil
}

func EnsureZcodeHooksEnabled(text string) (string, bool, error) {
	if strings.TrimSpace(text) == "" {
		text = "{}\n"
	}
	r := runtime{kind: kindJSON}
	view, root, err := rootObject(r, text)
	if err != nil {
		return "", false, err
	}
	hooksIndex := root.find("hooks")
	if hooksIndex < 0 {
		pad := padUnder(view, root, indentOf(view, root.close)+indent)
		return root.insert(text, renderJSONValuePath([]string{"hooks", "enabled"}, true, pad),
			indentOf(view, root.close)), true, nil
	}
	hooks, err := objectAt(view, root.members[hooksIndex].valueStart)
	if err != nil {
		return "", false, fmt.Errorf("hooks must be an object: %w", err)
	}
	enabledIndex := hooks.find("enabled")
	if enabledIndex >= 0 {
		member := hooks.members[enabledIndex]
		var enabled bool
		if err := json.Unmarshal([]byte(view[member.valueStart:member.end]), &enabled); err != nil {
			return "", false, fmt.Errorf("hooks.enabled must be a boolean")
		}
		return text, false, nil
	}
	pad := padUnder(view, hooks, indentOf(view, hooks.close)+indent)
	return hooks.insert(text, pad+quote("enabled")+": true", indentOf(view, hooks.close)), true, nil
}

func RemoveCreatedZcodeHooksEnabled(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return text, nil
	}
	r := runtime{kind: kindJSON}
	view, root, err := rootObject(r, text)
	if err != nil {
		return "", err
	}
	hooks, ok, err := objectAtPath(view, root, []string{"hooks"})
	if err != nil || !ok {
		return text, err
	}
	enabledIndex := hooks.find("enabled")
	if enabledIndex < 0 {
		return text, nil
	}
	member := hooks.members[enabledIndex]
	var enabled bool
	if err := json.Unmarshal([]byte(view[member.valueStart:member.end]), &enabled); err != nil {
		return "", fmt.Errorf("hooks.enabled must be a boolean")
	}
	if !enabled {
		return text, nil
	}
	return hooks.cut(text, enabledIndex), nil
}

func ZcodeHookCommands(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	view, root, err := rootObject(runtime{kind: kindJSON}, text)
	if err != nil {
		return nil, err
	}
	events, ok, err := objectAtPath(view, root, []string{"hooks", "events"})
	if err != nil || !ok {
		return nil, err
	}
	var commands []string
	for _, event := range events.members {
		entries, err := arrayAt(view, event.valueStart)
		if err != nil {
			return nil, fmt.Errorf("%s must be an array: %w", event.key, err)
		}
		for _, entry := range entries.values {
			group, err := objectAt(view, entry.start)
			if err != nil {
				return nil, err
			}
			hooksIndex := group.find("hooks")
			if hooksIndex < 0 {
				return nil, fmt.Errorf("%s hook group has no hooks array", event.key)
			}
			hooks, err := arrayAt(view, group.members[hooksIndex].valueStart)
			if err != nil {
				return nil, err
			}
			for _, candidate := range hooks.values {
				hook, err := objectAt(view, candidate.start)
				if err != nil {
					return nil, err
				}
				command := jsonStringMember(view, hook, "command")
				if command == "" {
					return nil, fmt.Errorf("%s hook has no command", event.key)
				}
				commands = append(commands, command)
			}
		}
	}
	return commands, nil
}

func RemoveZcodeSessionStartHook(text, marker string) (string, error) {
	if marker == "" {
		return "", fmt.Errorf("ZCode SessionStart marker must not be empty")
	}
	preimage := zcodeHookPreimage{}
	for {
		next, recorded, found, err := removeOneZcodeSessionStartHook(text, marker)
		if err != nil {
			return "", err
		}
		if !found {
			break
		}
		preimage.Hooks = preimage.Hooks || recorded.Hooks
		preimage.Events = preimage.Events || recorded.Events
		preimage.SessionStart = preimage.SessionStart || recorded.SessionStart
		text = next
	}
	var err error
	r := runtime{kind: kindJSON}
	if preimage.SessionStart {
		text, err = cutJSONArrayMemberIfEmpty(r, text, []string{"hooks", "events"}, "SessionStart")
		if err != nil {
			return "", err
		}
	}
	if preimage.Events {
		text, err = cutJSONMemberIfEmpty(r, text, []string{"hooks"}, "events")
		if err != nil {
			return "", err
		}
	}
	if preimage.Hooks {
		text, err = cutJSONMemberIfEmpty(r, text, nil, "hooks")
	}
	return text, err
}

func removeOneZcodeSessionStartHook(text, marker string) (string, zcodeHookPreimage, bool, error) {
	if strings.TrimSpace(text) == "" {
		return text, zcodeHookPreimage{}, false, nil
	}
	view, root, err := rootObject(runtime{kind: kindJSON}, text)
	if err != nil {
		return "", zcodeHookPreimage{}, false, err
	}
	container, ok, err := objectAtPath(view, root, []string{"hooks", "events"})
	if err != nil || !ok {
		return text, zcodeHookPreimage{}, false, err
	}
	sessionIndex := container.find("SessionStart")
	if sessionIndex < 0 {
		return text, zcodeHookPreimage{}, false, nil
	}
	entries, err := arrayAt(view, container.members[sessionIndex].valueStart)
	if err != nil {
		return "", zcodeHookPreimage{}, false, fmt.Errorf("SessionStart must be an array: %w", err)
	}
	for entryIndex, entry := range entries.values {
		group, err := objectAt(view, entry.start)
		if err != nil {
			continue
		}
		preimage, owned := parseZcodeHookMarker(marker, jsonStringMember(view, group, "matcher"))
		if owned {
			return entries.cut(text, entryIndex), preimage, true, nil
		}
	}
	return text, zcodeHookPreimage{}, false, nil
}

type zcodeHookPreimage struct {
	Hooks        bool
	Events       bool
	SessionStart bool
}

func zcodeOwnedHookGroup(marker string, preimage zcodeHookPreimage, command string, timeoutMs int) zcodeHookGroup {
	return zcodeHookGroup{Matcher: renderZcodeHookMarker(marker, preimage), Hooks: []zcodeCommandHook{{
		Type: "command", Command: command, TimeoutMs: timeoutMs,
	}}}
}

func renderZcodeHookMarker(marker string, preimage zcodeHookPreimage) string {
	code := "none"
	if preimage.Hooks {
		code = "hes"
	} else if preimage.Events {
		code = "es"
	} else if preimage.SessionStart {
		code = "s"
	}
	return "^(?:.*|" + marker + "_" + code + ")$"
}

func parseZcodeHookMarker(marker, value string) (zcodeHookPreimage, bool) {
	if value == "^(?:.*|"+marker+")$" {
		return zcodeHookPreimage{}, true
	}
	prefix := "^(?:.*|" + marker + "_"
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, ")$") {
		return zcodeHookPreimage{}, false
	}
	switch strings.TrimSuffix(strings.TrimPrefix(value, prefix), ")$") {
	case "none":
		return zcodeHookPreimage{}, true
	case "s":
		return zcodeHookPreimage{SessionStart: true}, true
	case "es":
		return zcodeHookPreimage{Events: true, SessionStart: true}, true
	case "hes":
		return zcodeHookPreimage{Hooks: true, Events: true, SessionStart: true}, true
	default:
		return zcodeHookPreimage{}, false
	}
}

func cutJSONArrayMemberIfEmpty(r runtime, text string, path []string, key string) (string, error) {
	return cutJSONMember(r, text, path, key, func(view string, valueStart int) (bool, error) {
		inside, err := arrayAt(view, valueStart)
		return err == nil && len(inside.values) == 0, err
	})
}

type zcodeHookGroup struct {
	Matcher string             `json:"matcher"`
	Hooks   []zcodeCommandHook `json:"hooks"`
}

type zcodeCommandHook struct {
	Type      string `json:"type"`
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeoutMs"`
}

func renderJSONValuePath(keys []string, value any, pad string) string {
	if len(keys) == 1 {
		rendered, _ := json.MarshalIndent(value, pad, indent)
		return pad + quote(keys[0]) + ": " + string(rendered)
	}
	childPad := pad + indent
	return pad + quote(keys[0]) + ": {\n" +
		renderJSONValuePath(keys[1:], value, childPad) + "\n" + pad + "}"
}

func jsonStringMember(view string, object object, key string) string {
	i := object.find(key)
	if i < 0 {
		return ""
	}
	member := object.members[i]
	var value string
	_ = json.Unmarshal([]byte(view[member.valueStart:member.end]), &value)
	return value
}

func setJSONObjectFields(text, view string, object object, values fields) (string, error) {
	missing := make(fields, 0, len(values))
	type replacement struct {
		start, end int
		value      string
	}
	var replacements []replacement
	for _, value := range values {
		i := object.find(value.key)
		if i < 0 {
			missing = append(missing, value)
			continue
		}
		member := object.members[i]
		rendered := jsonScalar(value.value)
		if view[member.valueStart:member.end] != rendered {
			replacements = append(replacements, replacement{
				start: member.valueStart, end: member.end, value: rendered,
			})
		}
	}
	slices.SortFunc(replacements, func(a, b replacement) int { return b.start - a.start })
	for _, replacement := range replacements {
		text = text[:replacement.start] + replacement.value + text[replacement.end:]
	}
	if len(missing) == 0 {
		return text, nil
	}
	view, _, err := jsonView(runtime{kind: kindJSON}, text)
	if err != nil {
		return "", err
	}
	object, err = objectAt(view, object.open)
	if err != nil {
		return "", err
	}
	pad := padUnder(view, object, indentOf(view, object.close)+indent)
	members := make([]string, len(missing))
	for i, value := range missing {
		members[i] = pad + quote(value.key) + ": " + jsonScalar(value.value)
	}
	return object.insert(text, strings.Join(members, ",\n"), indentOf(view, object.close)), nil
}

// --- the object scanner ---

type object struct {
	members []member
	// open and close are the braces' own offsets.
	open, close int
}

type arrayValue struct {
	start, end int
}

type array struct {
	values      []arrayValue
	open, close int
}

func arrayAt(view string, open int) (array, error) {
	if open >= len(view) || view[open] != '[' {
		return array{}, fmt.Errorf("an array was expected at offset %d", open)
	}
	result := array{open: open}
	for i := open + 1; ; {
		i = skipSpace(view, i)
		if view[i] == ']' {
			result.close = i
			return result, nil
		}
		if view[i] == ',' {
			i++
			continue
		}
		end := skipValue(view, i)
		result.values = append(result.values, arrayValue{start: i, end: end})
		i = end
	}
}

func arrayPadUnder(view string, values array, fallback string) string {
	if len(values.values) > 0 {
		if pad := indentOf(view, values.values[0].start); pad != "" {
			return pad
		}
	}
	return fallback
}

func (a array) insert(text, rendered, closePad string) string {
	if len(a.values) == 0 {
		return text[:a.open+1] + "\n" + rendered + text[a.open+1:]
	}
	last := a.values[len(a.values)-1].end
	return text[:last] + ",\n" + rendered + text[last:]
}

func (a array) cut(text string, i int) string {
	switch {
	case len(a.values) == 1:
		return text[:a.open+1] + text[a.values[i].end:]
	case i > 0:
		return text[:a.values[i-1].end] + text[a.values[i].end:]
	default:
		between := text[a.values[0].end:a.values[1].start]
		comma := strings.IndexByte(between, ',')
		return text[:a.values[0].start] + text[a.values[0].end+comma+1:]
	}
}

// find is where a member sits in the object, or -1. An index and not the member
// itself, because removing one is done by position.
func (o object) find(key string) int {
	for i, m := range o.members {
		if m.key == key {
			return i
		}
	}
	return -1
}

// insert adds one already-rendered member after the last one, or between the
// braces when the object is empty.
func (o object) insert(text, rendered, closePad string) string {
	if len(o.members) == 0 {
		return text[:o.open+1] + "\n" + rendered + "\n" + closePad + text[o.close:]
	}
	last := o.members[len(o.members)-1].end
	return text[:last] + ",\n" + rendered + text[last:]
}

// cut removes the i-th member and the comma that joined it to its neighbours.
// Which side the comma is taken from makes non-empty-object insertion
// reversible. Empty-object insertion deliberately normalizes the whitespace
// between the braces; that bounded limitation is documented in docs/mcp.md.
func (o object) cut(text string, i int) string {
	switch {
	case len(o.members) == 1:
		return text[:o.open+1] + text[o.close:] // empty object, not a blank line
	case i > 0:
		return text[:o.members[i-1].end] + text[o.members[i].end:]
	default:
		between := text[o.members[0].end:o.members[1].start]
		comma := strings.IndexByte(between, ',')
		return text[:o.members[0].start] + text[o.members[0].end+comma+1:]
	}
}

// padUnder is the indentation new members of this object line up with: what its
// first member uses, or the given fallback when it is empty.
func padUnder(view string, o object, fallback string) string {
	if len(o.members) > 0 {
		if pad := indentOf(view, o.members[0].start); pad != "" {
			return pad
		}
	}
	return fallback
}

func rootObject(r runtime, text string) (string, object, error) {
	view, _, err := jsonView(r, text)
	if err != nil {
		return "", object{}, err
	}
	root, err := objectAt(view, skipSpace(view, 0))
	return view, root, err
}

// Nothing below this line scans anything jsonView has not already proved to be
// one valid JSON object, and nothing scans past that object's closing brace. So
// there is no unterminated string and no unclosed bracket to report: the only
// surprise left is a value that is not an object where one has to be, which is
// what objectAt answers for. Re-adding the other checks would be defending a
// second time against what the decoder already refused.

// objectAt scans the object whose opening brace sits at open.
func objectAt(view string, open int) (object, error) {
	if open >= len(view) || view[open] != '{' {
		return object{}, fmt.Errorf("an object was expected at offset %d", open)
	}
	result := object{open: open}
	seen := map[string]bool{}
	for i := open + 1; ; {
		i = skipSpace(view, i)
		if view[i] == '}' {
			result.close = i
			return result, nil
		}
		if view[i] == ',' {
			i++
			continue
		}
		start := i
		key, next := scanString(view, i)
		if seen[key] {
			return object{}, fmt.Errorf("duplicate object member %q", key)
		}
		seen[key] = true
		valueStart := skipSpace(view, skipSpace(view, next)+1)
		end := skipValue(view, valueStart)
		result.members = append(result.members,
			member{key: key, start: start, valueStart: valueStart, end: end})
		i = end
	}
}

func skipSpace(view string, i int) int {
	return i + len(view[i:]) - len(strings.TrimLeft(view[i:], " \t\r\n"))
}

// scanString decodes the string that opens at i and returns the offset just
// past its closing quote. It is a string the decoder accepted, so it decodes.
func scanString(view string, i int) (string, int) {
	for end := i + 1; ; end++ {
		if view[end] == '\\' {
			end++
			continue
		}
		if view[end] == '"' {
			var decoded string
			_ = json.Unmarshal([]byte(view[i:end+1]), &decoded)
			return decoded, end + 1
		}
	}
}

// skipValue returns the offset just past the value that starts at i.
func skipValue(view string, i int) int {
	switch view[i] {
	case '"':
		_, end := scanString(view, i)
		return end
	case '{', '[':
		return skipBracketed(view, i)
	}
	end := i
	for !strings.ContainsRune(",}] \t\r\n", rune(view[end])) {
		end++
	}
	return end
}

func skipBracketed(view string, i int) int {
	depth := 0
	for j := i; ; j++ {
		switch view[j] {
		case '"':
			_, end := scanString(view, j)
			j = end - 1
		case '{', '[':
			depth++
		case '}', ']':
			if depth--; depth == 0 {
				return j + 1
			}
		}
	}
}

// jsonView returns the offset-stable text the scanner works over and the
// decoded object: the document itself for strict JSON, and a comment-free copy
// of the same length for JSONC. It also proves the file is one JSON object with
// nothing after it, so a file that is not one is refused before anything is
// written to it.
func jsonView(r runtime, text string) (string, map[string]any, error) {
	view := text
	if r.kind == kindJSONC {
		view = blankComments(text)
	}
	decoder := json.NewDecoder(strings.NewReader(view))
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return "", nil, err
	}
	if document == nil {
		return "", nil, fmt.Errorf("the configuration is not a JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return "", nil, fmt.Errorf("there is more than one document in the file")
		}
		return "", nil, err
	}
	return view, document, nil
}

// blankComments replaces every JSONC comment with spaces of the same length, so
// that an offset in the view is the same offset in the real text. The newlines
// stay, so line numbers do not move either.
func blankComments(text string) string {
	out := []byte(text)
	for i := 0; i < len(out); i++ {
		switch {
		case out[i] == '"':
			// Straight over the string, so a `//` inside one is text and not a
			// comment. A backslash takes the byte after it with it.
			for i++; i < len(out) && out[i] != '"'; i++ {
				if out[i] == '\\' {
					i++
				}
			}
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '/':
			for ; i < len(out) && out[i] != '\n'; i++ {
				out[i] = ' '
			}
		case out[i] == '/' && i+1 < len(out) && out[i+1] == '*':
			for ; i < len(out); i++ {
				if out[i] == '*' && i+1 < len(out) && out[i+1] == '/' {
					out[i], out[i+1] = ' ', ' '
					i++
					break
				}
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
		}
	}
	return string(out)
}

// indentOf is the leading whitespace of the line the offset sits on, or nothing
// when there is anything else before it on that line.
func indentOf(text string, offset int) string {
	lead := text[strings.LastIndex(text[:offset], "\n")+1 : offset]
	if strings.TrimSpace(lead) != "" {
		return ""
	}
	return lead
}

// ReplaceMember sets, or with a nil value removes, one top-level member of a
// JSON document, leaving every byte outside that member exactly where it was.
// The member itself is re-serialized and lined up with the indentation the file
// already uses: whoever owns a member owns how it is spelled.
func ReplaceMember(text, key string, value any) (string, error) {
	if strings.TrimSpace(text) == "" {
		if value == nil {
			return text, nil
		}
		text = "{}\n"
	}
	view, root, err := rootObject(runtime{kind: kindJSON}, text)
	if err != nil {
		return "", err
	}
	i := root.find(key)
	if value == nil {
		if i < 0 {
			return text, nil
		}
		return root.cut(text, i), nil
	}
	// Own indentation when already there; neighbours' when new.
	pad := padUnder(view, root, indent)
	if i >= 0 {
		pad = indentOf(view, root.members[i].start)
	}
	rendered, err := json.MarshalIndent(value, pad, indent)
	if err != nil {
		return "", err
	}
	if i >= 0 {
		m := root.members[i]
		return text[:m.valueStart] + string(rendered) + text[m.end:], nil
	}
	return root.insert(text, pad+quote(key)+": "+string(rendered),
		indentOf(view, root.close)), nil
}
