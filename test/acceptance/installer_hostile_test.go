package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The installer's hostile cases, run against the real `install.sh` with a real
// shell, the way the suite runs everything else here.
//
// The consecrated scenarios cover the ones this product designed for: a run
// killed with -9 converges (F01-10), a healthy installation is recognized and
// not redone (F01-11), and a stranger's file at the target is named and not
// overwritten (F01-12). What is left are the failures of the machine underneath,
// and there the rule is the one the script's own header states: whatever goes
// wrong, it says `install.sh:` and it says nothing was installed.

// A prefix the operator cannot write to is every locked-down machine and every
// `--prefix /usr/local/bin` without sudo. What came out was a bare `mkdir:
// Permission denied` from a tool the operator never invoked, with no word about
// whether anything had been installed.
func TestAPrefixThatCannotBeWrittenToIsRefusedByTheInstaller(t *testing.T) {
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })

	output, err := runTheInstaller(t, nil, "--repo", "owner/name",
		"--prefix", filepath.Join(locked, "bin"))
	if err == nil {
		t.Skip("this filesystem let a read-only directory be written to")
	}
	if !strings.Contains(output, "install.sh:") {
		t.Errorf("the failure is not the installer's own:\n%s", output)
	}
	if !strings.Contains(output, "Nothing was installed") {
		t.Errorf("it does not say whether anything was installed:\n%s", output)
	}
	if !strings.Contains(output, locked) {
		t.Errorf("it does not name the prefix it could not write to:\n%s", output)
	}
}

// The other shape of the same refusal, and the one a full disk arrives as: the
// prefix is there and cannot be written in. It is refused before anything is
// downloaded, so the operator does not pay for a transfer that had nowhere to
// land.
func TestAPrefixThatExistsAndIsNotWritableIsRefusedBeforeTheDownload(t *testing.T) {
	prefix := t.TempDir()
	if err := os.Chmod(prefix, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(prefix, 0o700) })

	// A channel that is not there: reaching it at all would already be a failure
	// of the order these checks run in.
	output, err := runTheInstaller(t, nil, "--repo", "owner/name",
		"--api", "http://127.0.0.1:1", "--prefix", prefix)
	if err == nil {
		t.Skip("this filesystem let a read-only directory be written to")
	}
	if !strings.Contains(output, "I cannot write in "+prefix) {
		t.Errorf("the refusal does not name the prefix it cannot write in:\n%s", output)
	}
	if strings.Contains(output, "release channel") {
		t.Errorf("it went to the channel before checking where the binary goes:\n%s", output)
	}
}

// A platform the channel does not build for is named with what the operator's
// own `uname` said, and with the three that ARE built, so nobody goes looking
// for a network problem. The fake `uname` is how the case is reached from a
// machine that is one of the three.
func TestAPlatformTheChannelDoesNotBuildForIsNamed(t *testing.T) {
	fake := t.TempDir()
	uname := "#!/bin/sh\ncase \"$1\" in -s) echo SunOS;; -m) echo sparc;; esac\n"
	if err := os.WriteFile(filepath.Join(fake, "uname"), []byte(uname), 0o755); err != nil {
		t.Fatal(err)
	}

	output, err := runTheInstaller(t, []string{"PATH=" + fake + ":" + os.Getenv("PATH")},
		"--repo", "owner/name")
	if err == nil {
		t.Fatal("an unbuilt platform was installed for")
	}
	for _, wanted := range []string{"SunOS/sparc", "darwin-arm64", "linux-x64", "linux-arm64"} {
		if !strings.Contains(output, wanted) {
			t.Errorf("the refusal does not name %q:\n%s", wanted, output)
		}
	}
}

// A channel that is not answering at all is the operator's network, their VPN or
// GitHub being down, and it is not the same thing as a repository that is
// private. What the installer owes here is to name the request that failed and
// to leave the prefix exactly as it found it.
func TestAChannelThatDoesNotAnswerLeavesThePrefixAlone(t *testing.T) {
	prefix := t.TempDir()
	theirs := filepath.Join(prefix, "roca")
	if err := os.WriteFile(theirs, []byte("#!/bin/sh\necho mine\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Port 1 with nothing listening: a connection refused, not a 404.
	output, err := runTheInstaller(t, nil, "--repo", "owner/name",
		"--api", "http://127.0.0.1:1", "--prefix", prefix, "--force")
	if err == nil {
		t.Fatal("the installer succeeded against a channel that is not there")
	}
	if !strings.Contains(output, "install.sh:") {
		t.Errorf("the failure is not the installer's own:\n%s", output)
	}
	body, readErr := os.ReadFile(theirs)
	if readErr != nil || !strings.Contains(string(body), "mine") {
		t.Fatalf("the file at the target did not survive a failed run: %v %q", readErr, body)
	}
	if leftovers := stagedInThePrefix(t, prefix); len(leftovers) > 0 {
		t.Errorf("a failed run left %v in the prefix", leftovers)
	}
}

// A release whose asset endpoint cannot be reached at all — DNS, a refused
// connection, TLS — is the operator's network or a token a private repository
// refused, not a release that "publishes no artefact". The installer has to
// name the channel the way a down channel is named, and keep the token remedy
// for the anonymous case. The suite cannot kill a real endpoint hermetically:
// the conventional fallback URL always points at the real github.com, which
// answers 404 and so would turn a network failure into a "publishes no
// artefact" verdict. A curl that answers 000 — the status real curl reports
// when it never reaches an answer — stands in for the dead endpoint instead.
func TestAnUnreachableAssetEndpointNamesTheNetworkNotTheRelease(t *testing.T) {
	bin := t.TempDir()
	// The fake curl serves the release document api_get asks for (no -w) and
	// answers 000 for every asset fetch (-w '%{http_code}'), which is real
	// curl's status when it cannot connect.
	spy := `#!/bin/sh
has_w=0; want_out=0; out=
for a in "$@"; do
  case "$a" in
    -w) has_w=1 ;;
    -o) want_out=1 ;;
    *)
      if [ "$want_out" = 1 ]; then out="$a"; want_out=0; fi
      ;;
  esac
done
cat >/dev/null
if [ "$has_w" = 1 ]; then
  printf '000'
  exit 7
fi
cat > "$out" <<'JSON'
{"tag_name":"v9.9.9","assets":[{"name":"sentinel","url":"http://127.0.0.1:1/asset"}]}
JSON
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(spy), 0o755); err != nil {
		t.Fatal(err)
	}
	// This case is the anonymous one: the GITHUB_TOKEN remedy belongs here, and
	// only here. install.sh's die_network deliberately withholds it when a token
	// is already set (sending a token-holder off to set a token is the
	// afternoon-costing misdiagnosis api_get warns about), so the assertion on
	// "GITHUB_TOKEN" is only valid with the token cleared. Clearing it also makes
	// the case reproducible on a machine or a CI lane that carries a token in its
	// ambient environment, where it would otherwise take the token-set branch and
	// report "the token is set, so this is the network".
	env := []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"GITHUB_TOKEN=",
	}
	output, err := runTheInstaller(t, env, "--repo", "owner/name",
		"--prefix", t.TempDir(), "--force")
	if err == nil {
		t.Fatal("the installer succeeded against an asset endpoint that never answers")
	}
	if !strings.Contains(output, "install.sh:") {
		t.Errorf("the failure is not the installer's own:\n%s", output)
	}
	if !strings.Contains(output, "did not answer") {
		t.Errorf("a network failure does not name the channel:\n%s", output)
	}
	if !strings.Contains(output, "GITHUB_TOKEN") {
		t.Errorf("the private-repository remedy is missing from a network failure:\n%s", output)
	}
	if strings.Contains(output, "publishes no artefact") {
		t.Errorf("a network failure is blamed on the release:\n%s", output)
	}
}

// A token interpolated into curl's -H argument is visible to every local user
// that can inspect the process list. The installer may pass the header through
// curl's config stdin, but the secret itself must never become an argv entry.
func TestTheInstallerDoesNotPutTheTokenOnCurlArgv(t *testing.T) {
	bin := t.TempDir()
	argvLog := filepath.Join(t.TempDir(), "argv")
	stdinLog := filepath.Join(t.TempDir(), "stdin")
	spy := `#!/bin/sh
printf '%s\n' "$@" > "$ROCA_CURL_ARGV_LOG"
cat > "$ROCA_CURL_STDIN_LOG"
exit 22
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(spy), 0o755); err != nil {
		t.Fatal(err)
	}
	const token = "token-visible-in-ps"
	env := []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"GITHUB_TOKEN=" + token,
		"ROCA_CURL_ARGV_LOG=" + argvLog,
		"ROCA_CURL_STDIN_LOG=" + stdinLog,
	}
	_, err := runTheInstaller(t, env, "--repo", "owner/name", "--prefix", t.TempDir())
	if err == nil {
		t.Fatal("the installer succeeded after the curl spy refused the request")
	}
	argv, readErr := os.ReadFile(argvLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(argv), token) {
		t.Fatalf("GITHUB_TOKEN is visible on curl argv:\n%s", argv)
	}
	config, readErr := os.ReadFile(stdinLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(config), "Authorization: Bearer "+token) {
		t.Fatalf("curl did not receive the authenticated header through config stdin:\n%s", config)
	}
}

// The Linux runner does not have BSD mktemp and the dev box does not have GNU
// mktemp: the installer has to land between them, calling mktemp the one way
// both answer. What hid the bug is that BSD mktemp fills in the X's for `-t
// prefix` and falls back to a default TMPDIR, and GNU does neither, so the same
// script was green on macOS and dead on Linux with "too few X's in template".
//
// The strict stand-in below reproduces the two GNU refusals (a bare `-d` builds
// under TMPDIR and fails when TMPDIR is not there; `-t prefix` has to carry its
// own X's) so a slip back into BSD-only mktemp is red here too and not only on
// the Linux CI that runs once a day. A passing run gets past the working
// directory and reaches the channel, which is the step after it.
func TestTheInstallerBuildsItsWorkdirPortably(t *testing.T) {
	fake := `#!/bin/sh
use_t=0
template=
for a in "$@"; do
  case "$a" in
    -t) use_t=1 ;;
    -d|-q|-u) ;;
    -*) ;;
    *) template="$a" ;;
  esac
done
if [ "$use_t" = 1 ]; then
  case "$template" in
    *XXX*) ;;
    *) printf "mktemp: too few X's in template '%s'\n" "$template" >&2; exit 1 ;;
  esac
fi
if [ -z "$template" ]; then
  base="${TMPDIR:-/tmp}"
  if [ ! -d "$base" ]; then
    printf "mktemp: failed to create directory via template '%s/tmp.XXXXXX': No such file or directory\n" "$base" >&2
    exit 1
  fi
  template="$base/tmp.XXXXXX"
fi
case "$template" in
  *XXXXXX*) ;;
  *) printf "mktemp: too few X's in template '%s'\n" "$template" >&2; exit 1 ;;
esac
out="/tmp/gnu-mktemp.$$"
mkdir "$out" && printf '%s\n' "$out"
`
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "mktemp"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	bogus := filepath.Join(t.TempDir(), "no-such-tmpdir")
	env := []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"TMPDIR=" + bogus,
	}
	output, err := runTheInstaller(t, env, "--repo", "owner/name",
		"--api", "http://127.0.0.1:1", "--prefix", t.TempDir(), "--force")
	if err == nil {
		t.Fatal("the installer succeeded against a channel that is not there")
	}
	if strings.Contains(output, "mktemp") {
		t.Errorf("the installer died at mktemp instead of building its workdir portably:\n%s", output)
	}
	if !strings.Contains(output, "release channel") {
		t.Errorf("the installer did not reach the channel, which is the step after its workdir:\n%s", output)
	}
}

// --- helpers ---

// runTheInstaller runs the real script with a real shell and hands back
// everything it said. The environment is the caller's plus whatever the case
// needs, because these are failures of the machine and the machine is what is
// being varied.
func runTheInstaller(t *testing.T, environment []string, arguments ...string) (string, error) {
	t.Helper()
	command := exec.Command("sh", append([]string{theInstallerPath()}, arguments...)...)
	command.Env = append(os.Environ(), environment...)
	command.Env = append(command.Env, "HOME="+t.TempDir())
	output, err := command.CombinedOutput()
	return string(output), err
}

func stagedInThePrefix(t *testing.T, prefix string) []string {
	t.Helper()
	entries, err := os.ReadDir(prefix)
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".roca-install.") {
			found = append(found, entry.Name())
		}
	}
	return found
}
