package vector

import "testing"

func TestExpandedQueriesWrapTheRawQueryInStaticTemplates(t *testing.T) {
	got := ExpandedQueries("salud mental")
	if len(got) != 1+len(QuestionTemplates) || got[0] != "salud mental" {
		t.Fatalf("expanded = %v", got)
	}
	for _, want := range []string{
		"qué se habló sobre: salud mental",
		"cómo afectó salud mental",
		"what was discussed about salud mental",
	} {
		found := false
		for _, item := range got {
			if item == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing template %q in %v", want, got)
		}
	}
}

func TestExpandedQueriesSkipEmptyInput(t *testing.T) {
	if got := ExpandedQueries("  "); got != nil {
		t.Fatalf("empty expansion = %v", got)
	}
}
