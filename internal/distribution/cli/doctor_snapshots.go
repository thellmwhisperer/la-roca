package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const leftoverSnapshotPrefix = "roca-read-only-snapshot-"

type leftoverSnapshots struct {
	Root  string `json:"root"`
	Count int    `json:"count"`
	Bytes int64  `json:"bytes"`
}

func collectSnapshotDoctor() leftoverSnapshots {
	root := os.TempDir()
	return inspectLeftoverSnapshots(root)
}

func inspectLeftoverSnapshots(root string) leftoverSnapshots {
	residue := leftoverSnapshots{Root: root}
	entries, err := os.ReadDir(root)
	if err != nil {
		return residue
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), leftoverSnapshotPrefix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		size := leftoverDirSize(path)
		residue.Count++
		residue.Bytes += size
	}
	return residue
}

func leftoverDirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

func renderSnapshotDoctor(env *cliEnv, residue leftoverSnapshots) {
	if residue.Count == 0 {
		env.print("read-only snapshots: none leftover under %s", residue.Root)
		return
	}
	env.print("read-only snapshots: %d leftover directories · %d bytes under %s",
		residue.Count, residue.Bytes, residue.Root)
	env.print("      remedy: an interactive `roca doctor` offers to delete abandoned copies")
}

func (env *cliEnv) offerSnapshotCleanup(cmd *cobra.Command, residue leftoverSnapshots) error {
	if residue.Count == 0 || env.json || !terminalInput(cmd.InOrStdin()) {
		return nil
	}
	fmt.Fprintf(env.errOut, "Delete %d leftover read-only snapshot directories (%d bytes)? [y/N] ",
		residue.Count, residue.Bytes)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return fmt.Errorf("read snapshot cleanup consent: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		removed, bytes := removeLeftoverSnapshots(residue.Root)
		env.print("removed %d leftover read-only snapshot directories · %d bytes", removed, bytes)
		return nil
	case "", "n", "no":
		return nil
	default:
		return fmt.Errorf("snapshot cleanup %q is not valid; answer yes or no", strings.TrimSpace(line))
	}
}

func removeLeftoverSnapshots(root string) (int, int64) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, 0
	}
	removed := 0
	var bytes int64
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), leftoverSnapshotPrefix) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		size := leftoverDirSize(path)
		if err := os.RemoveAll(path); err != nil {
			continue
		}
		removed++
		bytes += size
	}
	return removed, bytes
}
