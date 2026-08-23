package rocacorpus

import (
	"context"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
)

func TestStorageLawRewriteHonorsCancellation(t *testing.T) {
	path := t.TempDir() + "/roca-corpus.db"
	if err := ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	db, err := bundledplugin.OpenDatabase(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE session_versions ADD COLUMN title TEXT`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := applyStorageLaw(ctx, path, false); err == nil {
		t.Fatal("storage-law rewrite ignored cancellation")
	}
	db, err = bundledplugin.OpenDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	present, err := columnExistsDB(context.Background(), db, "session_versions", "title")
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Fatal("canceled storage-law rewrite changed the database")
	}
}

func TestVacuumHonorsCancellation(t *testing.T) {
	path := t.TempDir() + "/roca-corpus.db"
	if err := ApplySchema(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := vacuumDatabase(ctx, path); err == nil {
		t.Fatal("VACUUM ignored cancellation")
	}
}
