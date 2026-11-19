package toon

import "testing"

func TestQuoteEscapesControlCharacters(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"escape", "a\x1bb", `"a\u001bb"`},
		{"bell", "a\x07b", `"a\u0007b"`},
		{"backspace", "a\x08b", `"a\u0008b"`},
		{"carriage return", "a\rb", `"a\rb"`},
		{"tab", "a\tb", `"a\tb"`},
		{"newline", "a\nb", `"a\nb"`},
		{"backslash", `a\b`, `"a\\b"`},
		{"double quote", `a"b`, `"a\"b"`},
		{"plain", "hello", `"hello"`},
		{"number", "123", `"123"`},
		{"null literal", "null", `"null"`},
		{"leading dash", "-x", `"-x"`},
		{"leading hash", "#x", `"#x"`},
		{"comma", "a,b", `"a,b"`},
		{"empty", "", `""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Quote(tc.in); got != tc.want {
				t.Fatalf("Quote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStringQuotesControlCharacters(t *testing.T) {
	if got := String("a\x1bb"); got != `"a\u001bb"` {
		t.Fatalf("String with ESC = %q, want %q", got, `"a\u001bb"`)
	}
	if got := String("plain"); got != "plain" {
		t.Fatalf("String plain = %q, want plain", got)
	}
}

func TestNeedsQuotesControlCharacter(t *testing.T) {
	if !NeedsQuotes("a\x1bb") {
		t.Fatal("NeedsQuotes did not flag a control character")
	}
	if NeedsQuotes("hello") {
		t.Fatal("NeedsQuotes flagged a plain word")
	}
}

func TestRenderHelp(t *testing.T) {
	if got := RenderHelp(); got != "help[0]:" {
		t.Fatalf("RenderHelp with no lines = %q, want %q", got, "help[0]:")
	}
	got := RenderHelp("line one", "line two")
	want := "help[2]:\n  - line one\n  - line two"
	if got != want {
		t.Fatalf("RenderHelp = %q, want %q", got, want)
	}
}

func TestKey(t *testing.T) {
	if got := Key("hello"); got != "hello" {
		t.Fatalf("Key plain = %q", got)
	}
	if got := Key("hello world"); got != `"hello world"` {
		t.Fatalf("Key with space = %q", got)
	}
	if got := Key("_private"); got != "_private" {
		t.Fatalf("Key underscore = %q", got)
	}
	if got := Key("a.b"); got != "a.b" {
		t.Fatalf("Key dot = %q", got)
	}
}
