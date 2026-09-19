package ingest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/pkg/parsers"
)

// QualifySessionID namespaces a remote session identity so the same native id
// on two machines does not share a primary key. Local ids stay unchanged.
func QualifySessionID(machine, id string) string {
	machine = strings.TrimSpace(machine)
	if machine == "" || id == "" {
		return id
	}
	return machine + "/" + id
}

// NewestModTime is the mtime of the newest regular file under root.
func NewestModTime(root string) (time.Time, bool) {
	if strings.TrimSpace(root) == "" {
		return time.Time{}, false
	}
	var newest time.Time
	found := false
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		if !found || info.ModTime().After(newest) {
			newest = info.ModTime()
			found = true
		}
		return nil
	})
	return newest, found
}

// MirrorStale reports whether the newest file under root is older than hours.
// A missing root or an empty tree is stale: nothing has arrived.
func MirrorStale(root string, hours int, now time.Time) bool {
	if hours <= 0 {
		hours = DefaultStaleAfterHours
	}
	if _, err := os.Stat(root); err != nil {
		return true
	}
	newest, found := NewestModTime(root)
	if !found {
		return true
	}
	return now.Sub(newest) > time.Duration(hours)*time.Hour
}

func owningRoots(opts Options, target Target) Roots {
	if target.Remote {
		for _, remote := range opts.Roots.Remotes {
			if remote.Machine == target.Machine && remote.Home == target.RootHome {
				return remote
			}
		}
	}
	return opts.Roots
}

func labelRecords(target Target, records *parsers.Records) {
	if target.Machine == "" {
		return
	}
	for i := range records.Sessions {
		session := &records.Sessions[i]
		session.Machine = target.Machine
		if !target.Remote {
			continue
		}
		session.ID = QualifySessionID(target.Machine, session.ID)
		if session.ParentID != "" {
			session.ParentID = QualifySessionID(target.Machine, session.ParentID)
		}
	}
}
