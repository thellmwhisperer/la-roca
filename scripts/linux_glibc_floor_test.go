package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// glibcFloor runs the floor script with a stub readelf first on PATH that
// prints each file as its version-needs section.
func glibcFloor(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	stubs := filepath.Join(dir, "stubs")
	write(t, filepath.Join(stubs, "readelf"), "#!/bin/sh\nexec cat \"$3\"\n")
	if err := os.Chmod(filepath.Join(stubs, "readelf"), 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for name, needs := range files {
		write(t, filepath.Join(dir, name), needs)
		paths = append(paths, filepath.Join(dir, name))
	}
	cmd := exec.Command("sh", append([]string{"linux-glibc-floor.sh"}, paths...)...)
	cmd.Env = append(os.Environ(), "PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const roca194VectorNeeds = `Version needs section '.gnu.version_r' contains 2 entries:
 Addr: 0x0000000000000ab8  Offset: 0x000ab8  Link: 7 (.dynstr)
  000000: Version: 1  File: libm.so.6  Cnt: 2
  0x0010:   Name: GLIBC_2.43  Flags: none  Version: 9
  0x0020:   Name: GLIBC_2.2.5  Flags: none  Version: 4
  0x0030: Version: 1  File: libc.so.6  Cnt: 3
  0x0040:   Name: GLIBC_2.38  Flags: none  Version: 8
  0x0050:   Name: GLIBC_2.34  Flags: none  Version: 7
  0x0060:   Name: GLIBC_PRIVATE  Flags: none  Version: 6
`

const floorVectorNeeds = `Version needs section '.gnu.version_r' contains 1 entry:
  000000: Version: 1  File: libc.so.6  Cnt: 3
  0x0010:   Name: GLIBC_2.34  Flags: none  Version: 5
  0x0020:   Name: GLIBC_2.17  Flags: none  Version: 3
  0x0030:   Name: GLIBC_2.2.5  Flags: none  Version: 2
`

// A CGO_ENABLED=0 core has no dynamic section, so readelf prints no needs.
const staticCoreNeeds = "\nNo version information found in this file.\n"

func TestLinuxGlibcFloorRefusesTheV194CompanionAboveTheFloor(t *testing.T) {
	out, err := glibcFloor(t, map[string]string{"roca-vector": roca194VectorNeeds})
	if err == nil {
		t.Fatalf("the floor script passed a GLIBC_2.43 binary:\n%s", out)
	}
	for _, want := range []string{"roca-vector GLIBC_2.43\n", "above the floor GLIBC_2.35"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the floor script output lacks %q:\n%s", want, out)
		}
	}
}

func TestLinuxGlibcFloorPassesBinariesWithinTheFloor(t *testing.T) {
	out, err := glibcFloor(t, map[string]string{"roca-vector": floorVectorNeeds, "roca": staticCoreNeeds})
	if err != nil {
		t.Fatalf("the floor script refused binaries within the floor: %v\n%s", err, out)
	}
	for _, want := range []string{"roca-vector GLIBC_2.34\n", "roca none\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the floor script output lacks %q:\n%s", want, out)
		}
	}
}
