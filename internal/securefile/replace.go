package securefile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

var (
	errAtomicExchangeUnsupported = errors.New("atomic exchange publication is unsupported")
	exchangeFile                 = exchange
)

// replace publishes data at path only while path still holds previous (and,
// when original is set, that same regular file). The live path always names a
// complete file: the new inode is exchanged in, then the inode it displaced is
// checked, so an edit that landed after the first check is swapped back rather
// than clobbered. The result is the identity of the inode this call published.
//
// ponytail: a crash between the exchange and the check leaves the displaced
// file at a visible <name>.roca.swap-* beside path; nothing replays it.
func replace(path string, data, previous []byte, original os.FileInfo, mode os.FileMode) (os.FileInfo, error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".roca.swap-*")
	if err != nil {
		return nil, err
	}
	staged := temporary.Name()
	published, err := stage(temporary, data, mode)
	if err == nil {
		err = expect(path, previous, original)
	}
	if err == nil {
		err = exchangeFile(staged, path)
	}
	if err != nil {
		// Nothing was exchanged: the staged file is still ours to discard.
		os.Remove(staged)
		if errors.Is(err, errAtomicExchangeUnsupported) {
			return nil, fmt.Errorf("cannot safely replace %s: %w", path, err)
		}
		return nil, err
	}
	if err := expect(staged, previous, original); err != nil {
		return nil, restore(path, staged, published, err)
	}
	if err := os.Remove(staged); err != nil {
		return nil, err
	}
	return published, syncDir(filepath.Dir(path))
}

// stage makes data and mode durable in temporary, closes it, and returns the
// identity of the inode that will be published.
func stage(temporary *os.File, data []byte, mode os.FileMode) (os.FileInfo, error) {
	err := temporary.Chmod(mode)
	if err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	var info os.FileInfo
	if err == nil {
		info, err = temporary.Stat()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	return info, err
}

// restore swaps a displaced concurrent edit back to path. The staged name is
// removed only when it holds the inode this call published.
func restore(path, staged string, published os.FileInfo, changed error) error {
	if err := exchangeFile(staged, path); err != nil {
		return fmt.Errorf("%w; the displaced file is %s: %v", changed, staged, err)
	}
	if back, err := os.Lstat(staged); err != nil || !os.SameFile(back, published) {
		return fmt.Errorf("%w; the file that replaced it is kept at %s", changed, staged)
	}
	os.Remove(staged)
	return changed
}

func expect(path string, previous []byte, original os.FileInfo) error {
	if original != nil {
		if err := requireSameRegularFile(path, original); err != nil {
			return err
		}
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-read %s: %w", path, err)
	}
	if string(current) != string(previous) {
		return changedFileError(path)
	}
	if original != nil {
		return requireSameRegularFile(path, original)
	}
	return nil
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
