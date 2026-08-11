package release

import (
	"os"
	"strings"
	"testing"
)

// The artefact name is a contract shared by four places: this package, the
// Makefile that builds the artefacts, `install.sh` that downloads them and the
// workflow that publishes them. A fifth spelling anywhere turns "there is no
// artefact for your platform" into a lie on a platform that has one.
//
// Three of those four are held together here by reading the files themselves,
// because a comment saying "keep these in sync" has never kept anything in sync.
// The fourth (`install.sh`) builds its name at run time out of the tag and the
// platform, and the acceptance suite runs the real script.

// theMatrix is what the channel publishes, spelled as `Platform` spells it.
var theMatrix = []string{"darwin-arm64", "linux-x64", "linux-arm64", "windows-x64"}

// The Makefile is where the names are really written, and `make dist` is what
// the workflow runs. Reading it with the Makefile's own `$(VERSION)` in the
// place of a version is what makes the comparison exact instead of a substring
// that would also match a shorter platform's name.
func TestTheMakefileBuildsEveryArtefactTheChannelPublishes(t *testing.T) {
	makefile := readRepoFile(t, "../../../Makefile")
	for _, platform := range theMatrix {
		if want := ArtefactName("$(VERSION)", platform); !strings.Contains(makefile, want) {
			t.Errorf("the Makefile builds no %s", want)
		}
	}
}

// The workflow publishes what the Makefile built. A `go build` of its own in
// the channel would be a second spelling of the names, in the one place where
// nobody runs the tests that would catch it.
func TestTheChannelBuildsThroughTheMakefileAndNotByHand(t *testing.T) {
	workflow := readRepoFile(t, "../../../.github/workflows/release.yml")
	if !strings.Contains(workflow, "make dist") {
		t.Error("the release workflow does not build with `make dist`")
	}
	if strings.Contains(workflow, "go build") {
		t.Error("the release workflow builds by hand: the artefact names live in the Makefile")
	}
	// The checksums are what every install and every update verifies against,
	// and an artefact with no line of its own in them is refused by name.
	if !strings.Contains(workflow, "checksums.txt") {
		t.Error("the release workflow publishes no checksums.txt")
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
