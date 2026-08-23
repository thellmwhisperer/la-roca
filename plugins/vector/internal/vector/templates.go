package vector

import "strings"

// QuestionTemplates are the static wrappers that turn a bare noun query into
// the question-shaped embeddings that the live corpus actually ranks. They are
// never model-generated.
var QuestionTemplates = []string{
	"qué se habló sobre: %s",
	"cómo afectó %s",
	"what was discussed about %s",
}

// ExpandedQueries is the raw query plus every question template wrapping it.
func ExpandedQueries(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	out := make([]string, 0, 1+len(QuestionTemplates))
	out = append(out, query)
	seen := map[string]bool{query: true}
	for _, template := range QuestionTemplates {
		text := strings.TrimSpace(strings.ReplaceAll(template, "%s", query))
		if text == "" || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	return out
}
