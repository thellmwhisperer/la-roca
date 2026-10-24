package securefile_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/thellmwhisperer/la-roca/internal/securefile"
)

func TestTryLockDoesNotWaitAndCanBeReacquired(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".held")
	release, err := securefile.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := securefile.TryLock(path)
	if !errors.Is(err, securefile.ErrHeld) || unlock != nil {
		t.Fatalf("busy lock = %v, unlock present = %v", err, unlock != nil)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	unlock, err = securefile.TryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	release, err = securefile.LockExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}
