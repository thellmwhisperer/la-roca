package agentcfg

import (
	"encoding/json"
	"fmt"
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
		// new member of the container object.
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
	return jsonRemoveCreated(r, text, entries, nil)
}

func jsonRemoveCreated(r runtime, text string, entries, created []string) (string, error) {
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
	if removed == 0 || removed != len(inside.members) {
		return text, nil
	}
	serversPath := strings.Join(append(append([]string{}, r.parents...), r.serversKey), ".")
	// Top-level serversKey still uses emptiness: that is the existing Claude,
	// OpenCode and Pi contract. Nested parents prune only what this install
	// recorded as created.
	if len(r.parents) == 0 || containsString(created, serversPath) {
		text, err = cutJSONMemberAtPath(r, text, r.parents, r.serversKey)
		if err != nil {
			return "", err
		}
	}
	if len(r.parents) == 0 {
		return text, nil
	}
	for i := len(r.parents) - 1; i >= 0; i-- {
		if !containsString(created, strings.Join(r.parents[:i+1], ".")) {
			continue
		}
		view, root, err = rootObject(r, text)
		if err != nil {
			return "", err
		}
		parent, found, err := objectAtPath(view, root, r.parents[:i])
		if err != nil || !found {
			return text, err
		}
		childIndex := parent.find(r.parents[i])
		if childIndex < 0 {
			break
		}
		child, err := objectAt(view, parent.members[childIndex].valueStart)
		if err != nil || len(child.members) != 0 {
			break
		}
		text = parent.cut(text, childIndex)
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
	view, root, err := rootObject(r, text)
	if err != nil {
		return "", err
	}
	container, ok, err := objectAtPath(view, root, path)
	if err != nil || !ok {
		return text, err
	}
	i := container.find(key)
	if i < 0 {
		return text, nil
	}
	return container.cut(text, i), nil
}

func missingServerContainers(r runtime, text string) []string {
	if len(r.parents) == 0 {
		return nil
	}
	keys := append(append([]string{}, r.parents...), r.serversKey)
	if strings.TrimSpace(text) == "" {
		return containerPaths(keys)
	}
	view, root, err := rootObject(r, text)
	if err != nil {
		return nil
	}
	current := root
	for i, key := range keys {
		idx := current.find(key)
		if idx < 0 {
			return containerPaths(keys[i:], keys[:i]...)
		}
		next, err := objectAt(view, current.members[idx].valueStart)
		if err != nil {
			return nil
		}
		current = next
	}
	return nil
}

func containerPaths(keys []string, prefix ...string) []string {
	out := make([]string, 0, len(keys))
	path := append([]string{}, prefix...)
	for _, key := range keys {
		path = append(path, key)
		out = append(out, strings.Join(path, "."))
	}
	return out
}

func containsString(list []string, want string) bool {
	return slices.Contains(list, want)
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

// --- the object scanner ---

type object struct {
	members []member
	// open and close are the braces' own offsets.
	open, close int
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
// Which side the comma is taken from is what makes this reversible: a member
// appended after the last one is removed together with the comma that was added
// with it, so installing and withdrawing gives back the exact previous bytes.
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
	if err := decoder.Decode(new(any)); err == nil {
		return "", nil, fmt.Errorf("there is more than one document in the file")
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
