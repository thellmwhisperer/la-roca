package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestAdoptRepairsAnIndexDroppedAfterItsFirstInspect(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO memories(layer, content, origin)
			VALUES ('project', 'anchor of adoption', 'agent')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	other, err := Open(db.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	dropRequested := make(chan struct{})
	dropResult := make(chan error, 1)
	dropDone := make(chan struct{})
	go func() {
		defer close(dropDone)
		<-dropRequested
		_, err := other.SQL().ExecContext(ctx, `DROP INDEX IF EXISTS idx_memories_layer`)
		dropResult <- err
	}()

	first := true
	inspect := func(ctx context.Context, got *DB) (Report, error) {
		report, err := Inspect(ctx, got)
		if !first {
			return report, err
		}
		first = false
		close(dropRequested)
		dropErr := <-dropResult
		if err != nil {
			return report, err
		}
		if dropErr != nil {
			return report, dropErr
		}
		if report.Verdict != VerdictCurrent {
			t.Errorf("first inspect = %q (%s), want current", report.Verdict, report.Reason)
		}
		return report, nil
	}

	adoption, err := adoptWithInspect(ctx, db, t.TempDir(), inspect)
	<-dropDone
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if !adoption.Adopted {
		t.Fatal("Adopted = false, want true")
	}
	after, err := Inspect(ctx, db)
	if err != nil {
		t.Fatalf("Inspect after Adopt: %v", err)
	}
	if after.Verdict != VerdictCurrent {
		t.Fatalf("after Adopt = %q (%s), want current", after.Verdict, after.Reason)
	}
}

func TestApplyMigratableRepairsAGapAfterACurrentInspect(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "roca.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := ApplySchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO memories(layer, content, origin)
			VALUES ('project', 'anchor of adoption', 'agent')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	before, err := Inspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if before.Verdict != VerdictCurrent {
		t.Fatalf("before = %q (%s), want current", before.Verdict, before.Reason)
	}

	if _, err := db.SQL().ExecContext(ctx, `DROP INDEX idx_memories_layer`); err != nil {
		t.Fatal(err)
	}
	gap, err := Inspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if gap.Verdict != VerdictMigratable {
		t.Fatalf("gap = %q (%s), want migratable", gap.Verdict, gap.Reason)
	}

	adoption := Adoption{Report: before}
	if err := applyMigratable(ctx, db, t.TempDir(), &adoption, gap); err != nil {
		t.Fatalf("applyMigratable: %v", err)
	}
	after, err := Inspect(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if after.Verdict != VerdictCurrent {
		t.Fatalf("after = %q (%s), want current", after.Verdict, after.Reason)
	}
	if len(adoption.Repairs) == 0 {
		t.Fatal("Repairs empty, want the missing index")
	}
}
