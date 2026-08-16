package parsers

import (
	"os"
	"strings"
	"testing"
)

func TestGLMSkillKeepsItsCompleteSyntheticDocument(t *testing.T) {
	content, err := os.ReadFile("testdata/conformance/glm-skill/skill.data")
	if err != nil {
		t.Fatal(err)
	}
	records, err := Parse(KindGLMSkill, content, FileMeta{
		Path:     "/synthetic/home/.glm/skills/synthetic-compass/SKILL.md",
		FileName: "SKILL.md", SourceAgent: "glm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Memories) != 1 || records.Memories[0].Content != strings.TrimSpace(string(content)) {
		t.Fatalf("memories = %+v", records.Memories)
	}
}
