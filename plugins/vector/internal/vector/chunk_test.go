package vector

import (
	"strconv"
	"strings"
	"testing"
	"unicode"
)

func TestTokenChunksHonorSizeOverlapAndBoundaries(t *testing.T) {
	tokens := make([]string, 0, 500)
	for i := 0; i < 500; i++ {
		tokens = append(tokens, "w"+strconv.Itoa(i))
	}
	text := strings.Join(tokens, " ")

	got := tokenChunks(text, defaultChunkTokens, defaultOverlapTokens)
	if len(got) < 3 {
		t.Fatalf("500 tokens at 250/100 produced %d chunks, want at least 3", len(got))
	}

	first := strings.Fields(got[0])
	if len(first) != defaultChunkTokens {
		t.Fatalf("first chunk tokens = %d, want %d", len(first), defaultChunkTokens)
	}
	second := strings.Fields(got[1])
	if len(second) != defaultChunkTokens {
		t.Fatalf("second chunk tokens = %d, want %d", len(second), defaultChunkTokens)
	}
	overlap := defaultChunkTokens - defaultOverlapTokens
	if first[overlap] != second[0] || first[len(first)-1] != second[defaultOverlapTokens-1] {
		t.Fatalf("overlap boundary mismatch: first ends %q, second starts %q",
			first[len(first)-1], second[0])
	}

	last := strings.Fields(got[len(got)-1])
	if last[len(last)-1] != tokens[len(tokens)-1] {
		t.Fatalf("last chunk dropped the terminal token: %q", last[len(last)-1])
	}
	if strings.Fields(got[len(got)-2])[len(strings.Fields(got[len(got)-2]))-1] == last[len(last)-1] &&
		len(last) == defaultChunkTokens {
		t.Fatalf("terminal chunk repeated the previous window")
	}
}

func TestTokenChunksKeepAShortDocumentAsOneChunk(t *testing.T) {
	text := "short human question about rest"
	got := tokenChunks(text, defaultChunkTokens, defaultOverlapTokens)
	if len(got) != 1 || got[0] != text {
		t.Fatalf("short text chunks = %q", got)
	}
}

func TestTokenChunksRejectInvalidWindows(t *testing.T) {
	if tokenChunks("abc def", 0, 0) != nil || tokenChunks("abc def", 4, 4) != nil ||
		tokenChunks("", defaultChunkTokens, defaultOverlapTokens) != nil {
		t.Fatal("invalid windows must produce no chunks")
	}
}

func TestTokenChunksPreserveUnicodeWordBoundaries(t *testing.T) {
	text := "café 🙂 mañana"
	got := tokenChunks(text, 2, 1)
	if len(got) != 2 {
		t.Fatalf("unicode chunks = %q", got)
	}
	if !strings.Contains(got[0], "café") || !strings.Contains(got[0], "🙂") {
		t.Fatalf("first unicode chunk lost a word: %q", got[0])
	}
	if !strings.Contains(got[1], "🙂") || !strings.Contains(got[1], "mañana") {
		t.Fatalf("overlap did not carry the middle word: %q", got[1])
	}
	for _, chunk := range got {
		for _, r := range chunk {
			if unicode.IsSpace(r) {
				continue
			}
			if r == '🙂' || unicode.IsLetter(r) {
				continue
			}
			t.Fatalf("chunk introduced unexpected rune %q in %q", r, chunk)
		}
	}
}

func TestChunkHeaderUsesTitleAndYearMonth(t *testing.T) {
	if got := chunkHeader("Night shift rest", "2026-03-18T04:12:00Z"); got != "[Night shift rest · 2026-03] " {
		t.Fatalf("header = %q", got)
	}
	if got := chunkHeader("Night shift rest", ""); got != "[Night shift rest] " {
		t.Fatalf("title-only header = %q", got)
	}
	if got := chunkHeader("", "2026-03-18"); got != "[2026-03] " {
		t.Fatalf("date-only header = %q", got)
	}
	if got := chunkHeader("", "not-a-date"); got != "" {
		t.Fatalf("empty header = %q", got)
	}
}
