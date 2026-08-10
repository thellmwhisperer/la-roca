package ingest

import "testing"

func TestADirectoryNameThatIsNotAnEncodedPathIsTheProject(t *testing.T) {
	project, ok := ProjectFromEncodedDir("la-roca", WorkspaceRoots{})
	if !ok || project != "la-roca" {
		t.Errorf("project = %q, ok = %v", project, ok)
	}
}

func TestAnEncodedAbsolutePathDecodesAgainstTheConfiguredRoot(t *testing.T) {
	roots := ResolveWorkspaceRoots([]string{"/w"})
	project, ok := ProjectFromEncodedDir("-w-demo", roots)
	if !ok || project != "demo" {
		t.Errorf("project = %q, ok = %v", project, ok)
	}
}

// The encoding is lossy, so a directory no configured prefix explains is
// ambiguous. It is a diagnosis with a remedy, and no raw absolute path is
// persisted as if it were a project name (TECH-SPEC 5.1).
func TestAnEncodedPathNoRootExplainsIsAmbiguous(t *testing.T) {
	project, ok := ProjectFromEncodedDir("-w-otro-sitio-demo", ResolveWorkspaceRoots([]string{"/x"}))
	if ok {
		t.Errorf("it decoded %q out of a path nothing explains", project)
	}
	if project != "" {
		t.Errorf("project = %q, want empty", project)
	}
}

func TestTheLongestRootWins(t *testing.T) {
	roots := ResolveWorkspaceRoots([]string{"/w", "/w/teams/alpha"})
	project, ok := ProjectFromEncodedDir("-w-teams-alpha-demo", roots)
	if !ok || project != "demo" {
		t.Errorf("project = %q, ok = %v", project, ok)
	}
}

// A Windows workspace root is encoded with its drive letter, and matching it is
// case-insensitive because Windows paths are.
func TestAWindowsRootDecodesItsOwnEncoding(t *testing.T) {
	roots := ResolveWorkspaceRoots([]string{`C:\Users\ale\code`})
	for _, dir := range []string{"C--Users-ale-code-demo", "c--users-ale-code-demo"} {
		project, ok := ProjectFromEncodedDir(dir, roots)
		if !ok || project != "demo" {
			t.Errorf("%s: project = %q, ok = %v", dir, project, ok)
		}
	}
}

// The WSL case: under WSL the very same directory has two spellings, the Windows
// one and the /mnt/c one, and the agent may have encoded either. A root declared
// in one spelling has to explain a directory encoded in the other, or the project
// of every repository living on the Windows drive comes out ambiguous.
func TestAWorkspaceRootCrossingMntCDecodesBothSpellings(t *testing.T) {
	cases := []struct{ root, dir string }{
		{`C:\Users\ale\code`, "-mnt-c-Users-ale-code-demo"},
		{"/mnt/c/Users/ale/code", "C--Users-ale-code-demo"},
		{"/mnt/c/Users/ale/code", "-mnt-c-Users-ale-code-demo"},
		{`C:\Users\ale\code`, "C--Users-ale-code-demo"},
	}
	for _, one := range cases {
		roots := ResolveWorkspaceRoots([]string{one.root})
		project, ok := ProjectFromEncodedDir(one.dir, roots)
		if !ok || project != "demo" {
			t.Errorf("root %s with dir %s: project = %q, ok = %v", one.root, one.dir, project, ok)
		}
	}
}

func TestARootThatDecodesToAnotherEncodedPathIsNotAProject(t *testing.T) {
	// Decoding "/" against the root "/" would leave the whole encoded remainder,
	// which is a path and not a project name.
	roots := ResolveWorkspaceRoots([]string{"/"})
	if project, ok := ProjectFromEncodedDir("--w-demo", roots); ok {
		t.Errorf("it decoded %q", project)
	}
}

func TestWorkspaceRootsAreCanonicalizedAndDeduplicated(t *testing.T) {
	roots := ResolveWorkspaceRoots([]string{" /w ", "/w", "/w/", "", "/x"})
	if got := roots.Selected; len(got) != 2 || got[0] != "/w" || got[1] != "/x" {
		t.Errorf("selected = %v", got)
	}
}

func TestProjectFromCwdIsItsLastSegmentInEitherSyntax(t *testing.T) {
	cases := map[string]string{
		"/w/demo":           "demo",
		"/w/demo/":          "demo",
		`C:\Users\ale\demo`: "demo",
		"/mnt/c/code/demo":  "demo",
		"":                  "",
		"/":                 "",
	}
	for cwd, want := range cases {
		if got := ProjectFromCwd(cwd); got != want {
			t.Errorf("%q: %q, want %q", cwd, got, want)
		}
	}
}

// A metadata cwd that points at the runtime's own session store says nothing
// about a project: filing those sessions under "sessions" would invent one.
func TestProjectFromMetadataCwdIgnoresTheVendorsSessionRoot(t *testing.T) {
	if got := ProjectFromMetadataCwd("/sessions/abc123"); got != "" {
		t.Errorf("project = %q, want empty", got)
	}
	if got := ProjectFromMetadataCwd("/w/sessions/demo"); got != "demo" {
		t.Errorf("project = %q, want demo", got)
	}
}

func TestProjectFromPathDecodesTheDirectoryTheFileLivesIn(t *testing.T) {
	roots := ResolveWorkspaceRoots([]string{"/w"})
	project, ok := ProjectFromPath("/home/u/.claude/projects/-w-demo/session.jsonl", roots)
	if !ok || project != "demo" {
		t.Errorf("project = %q, ok = %v", project, ok)
	}
}
