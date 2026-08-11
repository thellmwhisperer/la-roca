package service

import (
	"encoding/json"
	"testing"
)

// The surface audit is the record of WHICH surface wrote a memory, and
// `surfaceKey` is documented as reserved: a caller's own metadata never
// overwrites it. The audit was only written when a surface was supplied, so a
// caller that declared its own `surface` on a path with none got that value
// stored verbatim: a forged audit, in the one field the caller may not author.
func TestTheCallerCannotAuthorTheSurfaceAudit(t *testing.T) {
	for _, want := range []struct {
		name    string
		surface string
		stored  any
	}{
		{name: "a real surface overwrites the forgery", surface: SurfaceCLI, stored: SurfaceCLI},
		{name: "no surface leaves no forgery behind", surface: "", stored: nil},
	} {
		t.Run(want.name, func(t *testing.T) {
			encoded, err := encodeMetadata(
				map[string]any{"surface": "forged", "note": "kept"}, want.surface)
			if err != nil {
				t.Fatalf("encodeMetadata: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
				t.Fatalf("the metadata is not a JSON object: %v", err)
			}
			if got := decoded[surfaceKey]; got != want.stored {
				t.Errorf("%s = %v, want %v", surfaceKey, got, want.stored)
			}
			// The caller's own unreserved keys are still theirs.
			if decoded["note"] != "kept" {
				t.Errorf("the caller's metadata was lost: %v", decoded)
			}
		})
	}
}
