package service

import "testing"

// Empty evidence stays explicit instead of becoming a plausible-looking guess.
func TestAuthorshipNormalizationNeverGuesses(t *testing.T) {
	got := (Authorship{}).normalized()
	want := Authorship{Agent: UnknownAuthor, Model: UnknownAuthor, Surface: UnknownAuthor}
	if got != want {
		t.Fatalf("normalized authorship = %+v, want %+v", got, want)
	}
}
