package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAgentMemoryUnchangedPassDoesNotOpenFiles(t *testing.T) {
	home := t.TempDir()
	roots := ResolveRoots(Environment{GOOS: "linux", Home: home}, Settings{})
	paths := []string{
		filepath.Join(roots.ZCodeMemories, "projects", "harbor", "memory", "chart.md"),
		filepath.Join(roots.GrokMemoryV2, "workspaces", "harbor", "MEMORY.md"),
	}
	w := &world{home: home}
	for _, path := range paths {
		w.write(t, path, "alpha\n")
	}
	ctx := context.Background()
	db := rocaDatabase(t)
	opts := Options{Roots: roots}
	first, err := Run(ctx, db, registry(t), opts)
	if err != nil || first.Errors != 0 || first.Delta.Memories != 2 {
		t.Fatalf("first ingest: %+v, %v", first, err)
	}
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	for _, path := range paths {
		if _, err := unix.InotifyAddWatch(fd, path, unix.IN_OPEN); err != nil {
			t.Fatal(err)
		}
	}
	second, err := Run(ctx, db, registry(t), opts)
	if err != nil || second.Errors != 0 || second.FilesSkipped != 2 || second.Delta.Memories != 0 {
		t.Fatalf("unchanged ingest: %+v, %v", second, err)
	}
	var events [4096]byte
	if n, err := unix.Read(fd, events[:]); n > 0 || !errors.Is(err, unix.EAGAIN) {
		t.Fatalf("unchanged ingest opened memory files: events=%d error=%v", n, err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		w.write(t, path, "bravo\n")
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := unix.Read(fd, events[:]); n <= 0 || err != nil {
		t.Fatalf("open watcher failed to observe edits: events=%d error=%v", n, err)
	}
	third, err := Run(ctx, db, registry(t), opts)
	if err != nil || third.Errors != 0 || third.Sources["zcode"].MemoriesUpdated != 1 ||
		third.Sources["grok"].MemoriesUpdated != 1 {
		t.Fatalf("same-size same-mtime edit: %+v, %v", third, err)
	}
	if n, err := unix.Read(fd, events[:]); n <= 0 || err != nil {
		t.Fatalf("changed ingest did not reopen files: events=%d error=%v", n, err)
	}
	if got := countRows(t, db.SQL(), `memories WHERE content = 'bravo'`); got != 2 {
		t.Fatalf("updated memories = %d, want 2", got)
	}
}
