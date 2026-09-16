package vector

import "testing"

func TestExpandedQueriesWrapTheRawQueryInStaticTemplates(t *testing.T) {
	got := ExpandedQueries("huerto urbano")
	if len(got) != 1+len(QuestionTemplates) || got[0] != "huerto urbano" {
		t.Fatalf("expanded = %v", got)
	}
	for _, want := range []string{
		"qué se habló sobre: huerto urbano",
		"cómo afectó huerto urbano",
		"what was discussed about huerto urbano",
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

func TestExpandWithUsesTheSuppliedTemplatesOrNone(t *testing.T) {
	got := ExpandWith("huerto urbano", []string{"about %s"})
	if len(got) != 2 || got[0] != "huerto urbano" || got[1] != "about huerto urbano" {
		t.Fatalf("custom expansion = %v", got)
	}
	raw := ExpandWith("huerto urbano", nil)
	if len(raw) != 1 || raw[0] != "huerto urbano" {
		t.Fatalf("empty templates should keep the raw query: %v", raw)
	}
}

func TestExpandedQueriesSkipEmptyInput(t *testing.T) {
	if got := ExpandedQueries("  "); got != nil {
		t.Fatalf("empty expansion = %v", got)
	}
}
