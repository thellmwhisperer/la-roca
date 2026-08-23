package search_test

import (
	"context"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// The proof is the difference between "the index exists" and "the index
// answers". It takes a word out of the data, asks the index for it, and the
// word it names has to be one the corpus actually contains.
func TestProofAsksTheIndexForAWordTheDatabaseHolds(t *testing.T) {
	_, db := indexedWorld(t)

	proof, err := search.Prove(context.Background(), db)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if !proof.Ready {
		t.Fatalf("word search did not answer over a built index: %+v", proof)
	}
	if proof.Word == "" || proof.Matches == 0 {
		t.Fatalf("the proof names no word or no match: %+v", proof)
	}
	if got := ftsCount(t, db, `"`+proof.Word+`"`); got == 0 {
		t.Fatalf("the proof reported %q, which the corpus does not contain", proof.Word)
	}
}

// A machine with no agent history has nothing to find. That is not a broken
// index, and the caller has to be able to tell the two apart.
func TestProofSaysEmptyRatherThanBrokenOnAFreshDatabase(t *testing.T) {
	ctx := context.Background()
	db := openWorld(t)
	if _, err := search.Index(ctx, db, nil); err != nil {
		t.Fatalf("Index: %v", err)
	}

	proof, err := search.Prove(ctx, db)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if proof.Ready || !proof.Empty {
		t.Fatalf("an empty database was not reported as empty: %+v", proof)
	}
	if proof.Reason == "" {
		t.Fatal("the empty verdict carries no reason")
	}
}

// An index that was never built is a fault with a name. Reporting it as empty
// would tell somebody with a full database that they have nothing.
func TestProofRefusesToCallAnEmptiedIndexEmpty(t *testing.T) {
	_, db := indexedWorld(t)
	ctx := context.Background()
	for _, index := range []string{"memories_fts", "exchanges_fts", "thinking_fts", "sessions_fts"} {
		writeTo(t, db, `INSERT INTO `+index+`(`+index+`) VALUES('delete-all')`)
	}

	proof, err := search.Prove(ctx, db)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if proof.Ready {
		t.Fatalf("an unbuilt index reported ready: %+v", proof)
	}
	if proof.Empty {
		t.Fatalf("a database with rows was reported as empty: %+v", proof)
	}
	if proof.Word == "" || proof.Reason == "" {
		t.Fatalf("the failure names neither the word nor the reason: %+v", proof)
	}
}

func TestProofSkipsANewerTokenlessRow(t *testing.T) {
	ctx := context.Background()
	db := openWorld(t)
	writeTo(t, db, `INSERT INTO memories (layer, content, origin) VALUES
		('discovery', 'alpha lighthouse', 'agent'),
		('discovery', '😀', 'agent')`)
	if _, err := search.Index(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	proof, err := search.Prove(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.Ready || proof.Empty || proof.Word != "lighthouse" || proof.Matches == 0 {
		t.Fatalf("tokenless newest row hid searchable history: %+v", proof)
	}
}
