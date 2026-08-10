// Package lifecycle is the deletion half of "installing is copying a binary".
//
// It exists to hold one contract, D-7, and the contract has two halves that
// look like they contradict each other and do not:
//
//   - **What Roca owns is deleted whenever it is there.** The inventory is a
//     DECLARATION and never a snapshot of the filesystem taken beforehand. That
//     is what killed the laboratory's purge in #451: it captured the inventory
//     before creating its own lock directory, then refused to delete that
//     directory as one that "appeared after the inventory", reported
//     `purged: no`, and left residue with the CLI already gone.
//   - **What Roca did not create is never deleted**, it is reported by name and
//     the directory holding it survives with it. That protection is kept whole.
//     What was removed is the race, not the protection.
//
// The consequence of the two together is a purge that converges: it can run on
// a machine a previous attempt left halfway, it can run twice, and both runs
// end ok.
package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BinaryName is the only file name this product will delete as its own binary.
const BinaryName = "roca"

// Plan is what one uninstall is allowed to remove. Nothing outside it is
// touched, which is why building it is the caller's job and applying it is not.
type Plan struct {
	// Owned are the exact paths Roca created: the database and its journals,
	// the configuration, the backups, the cache, the credentials, the benches.
	// Each one is deleted whenever it exists, whether it was there when the
	// plan was made or appeared afterwards.
	Owned []string
	// DataDir is the directory those live in. It is removed only when nothing
	// outside Owned is left inside it, so an adopted directory that also holds
	// somebody else's files survives with them.
	DataDir string
	// Binary is the executable to unlink, and it is only unlinked when it is
	// called `roca`.
	Binary string
}

// Kept is a path that was left alone, with the reason. The reason is the whole
// point: "kept" with no reason sends an operator to read code.
type Kept struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// Report is what the purge did. `Purged` is true when it ran to the end without
// an error, whether or not it found anything: finishing a job that was already
// finished is success, and reporting it as a failure is what made the lab's
// second purge look broken.
type Report struct {
	Purged  bool     `json:"purged"`
	Deleted []string `json:"deleted"`
	Kept    []Kept   `json:"kept,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// Apply removes what the plan declares.
//
// It never stops at the first problem: a purge that gives up halfway leaves a
// machine in a state neither the operator nor the next run can describe, and
// the whole reason this command is re-runnable is that a half-purged machine is
// a normal thing to find.
func (p Plan) Apply() Report {
	report := Report{Purged: true, Deleted: []string{}}

	sortByDepth(p.Owned)

	for _, path := range p.Owned {
		report.remove(path)
	}
	p.removeBinary(&report)
	p.removeDataDir(&report)

	sortByDepth(report.Deleted)
	return report
}

// sortByDepth orders paths children-before-parent, so an operator sees each
// file inside a directory before the directory itself.
func sortByDepth(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		a, b := paths[i], paths[j]
		if da, db := strings.Count(a, string(os.PathSeparator)),
			strings.Count(b, string(os.PathSeparator)); da != db {
			return da > db
		}
		return a > b
	})
}

// remove deletes one owned path, directory or file, and records which of the
// three things happened. A path that is not there is `absent`, and absent is
// not an error: it is the state after the previous run.
func (r *Report) remove(path string) {
	if path == "" {
		return
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return
	}
	if err := os.RemoveAll(path); err != nil {
		r.fail("delete %s: %v", path, err)
		return
	}
	r.Deleted = append(r.Deleted, path)
}

// removeBinary unlinks the executable, and only when it is Roca's.
//
// The name is the whole check on purpose. An operator who pointed this at
// something else asked for their own file to be deleted, and this command
// answers by naming what it expected instead of doing it.
func (p Plan) removeBinary(report *Report) {
	if p.Binary == "" {
		return
	}
	if name := filepath.Base(p.Binary); name != BinaryName && name != BinaryName+".exe" {
		report.Kept = append(report.Kept, Kept{
			Path:   p.Binary,
			Reason: fmt.Sprintf("this is not a roca binary: it is called %q", name),
		})
		return
	}
	report.remove(p.Binary)
}

// removeDataDir takes the directory away when nothing that is not Roca's is
// left in it, and reports by name whatever kept it alive.
//
// The list of survivors is bounded on purpose: an operator whose Roca directory
// also holds two hundred files of their own needs to know that and not to read
// two hundred lines.
func (p Plan) removeDataDir(report *Report) {
	if p.DataDir == "" {
		return
	}
	entries, err := os.ReadDir(p.DataDir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		report.fail("read %s: %v", p.DataDir, err)
		return
	}
	if len(entries) == 0 {
		report.remove(p.DataDir)
		return
	}

	owned := map[string]bool{}
	for _, path := range p.Owned {
		owned[path] = true
	}

	const named = 5
	for index, entry := range entries {
		if index == named {
			report.Kept = append(report.Kept, Kept{
				Path: p.DataDir,
				Reason: fmt.Sprintf("and %d more files La Roca did not create",
					len(entries)-named),
			})
			break
		}
		path := filepath.Join(p.DataDir, entry.Name())
		report.Kept = append(report.Kept, Kept{Path: path, Reason: whyItStayed(owned[path])})
	}
}

// whyItStayed tells the two survivors apart, because the operator does two
// different things with them.
//
// A path on the inventory that is still here is Roca's and the purge failed to
// remove it: a live process wrote its journal back after the sweep went past, or
// the directory stopped being writable. Telling the operator that La Roca did
// not create their own database is the second half of D-7 firing at the first
// half's files, and it sends them to delete this product by hand.
func whyItStayed(isOurs bool) string {
	if isOurs {
		return "La Roca created it and could not delete it: run the uninstall again"
	}
	return "La Roca did not create it: delete it yourself if you want to"
}

func (r *Report) fail(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
	r.Purged = false
}
