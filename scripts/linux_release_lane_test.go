package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLinuxReleaseLane runs one Linux leg over stub make and readelf: the stub
// make logs what it was asked to build, the core stub materializes a
// roca-vector that answers a version, and readelf reports the glibc need.
func TestLinuxReleaseLane(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, target, arch, version, reports, need, make string
		passes                                           bool
		want                                             []string
	}{
		{"amd64 within the floor, version from git describe", "linux-amd64", "x64", "", "v9.9.9", "2.34",
			"linux-amd64 VERSION=v9.9.9\nwindows-amd64 VERSION=v9.9.9\n", true,
			[]string{"roca-vector v9.9.9\n", "roca-v9.9.9-linux-x64 none\n", "bin/roca-vector GLIBC_2.34\n", "vector/roca-vector GLIBC_2.34\n"}},
		{"arm64 above the floor", "linux-arm64", "arm64", "v9.9.9", "v9.9.9", "2.43",
			"linux-arm64 VERSION=v9.9.9\n", false,
			[]string{"bin/roca-vector GLIBC_2.43\n", "above the floor GLIBC_2.35"}},
		{"a companion that answers another version", "linux-arm64", "arm64", "v9.9.9", "v1.0.0", "2.34",
			"linux-arm64 VERSION=v9.9.9\n", false,
			[]string{"reports 'roca-vector v1.0.0' and the build is v9.9.9"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			command(t, dir, nil, "git", "init", "-q")
			command(t, dir, nil, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
			command(t, dir, nil, "git", "tag", "v9.9.9")
			stubs := filepath.Join(dir, "stubs")
			vector := "#!/bin/sh\necho 'roca-vector " + c.reports + "'\n"
			executables := map[string]string{
				filepath.Join(stubs, "make"):                           "#!/bin/sh\necho \"$@\" >> make.log\n",
				filepath.Join(stubs, "readelf"):                        "#!/bin/sh\ncase \"$3\" in *roca-vector) echo '  Name: GLIBC_" + c.need + "  Flags: none' ;; esac\n",
				filepath.Join(dir, "bin", "roca-v9.9.9-linux-"+c.arch): "#!/bin/sh\ncp \"$VECTOR_STUB\" \"$ROCA_PREFIX/roca-vector\"\n",
				filepath.Join(dir, "package", "roca-vector"):           vector,
			}
			for path, body := range executables {
				write(t, path, body)
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			command(t, dir, nil, "tar", "-C", "package", "-czf", "bin/roca-vector-v9.9.9-linux-"+c.arch+".tar.gz", ".")

			cmd := exec.Command("sh", filepath.Join(root, "scripts", "linux-release-lane.sh"), c.target, c.version)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"PATH="+stubs+string(os.PathListSeparator)+os.Getenv("PATH"),
				"VECTOR_STUB="+filepath.Join(dir, "package", "roca-vector"))
			out, err := cmd.CombinedOutput()
			if (err == nil) != c.passes {
				t.Fatalf("the lane exited with %v, want passing=%v:\n%s", err, c.passes, out)
			}
			for _, want := range c.want {
				if !strings.Contains(string(out), want) {
					t.Fatalf("the lane output lacks %q:\n%s", want, out)
				}
			}
			if logged, _ := os.ReadFile(filepath.Join(dir, "make.log")); string(logged) != c.make {
				t.Fatalf("make was asked for %q, want %q", logged, c.make)
			}
			if _, err := os.Stat(filepath.Join(dir, "release-"+c.target+".tar.gz")); (err == nil) != c.passes {
				t.Fatalf("the lane archive exists=%v, want %v", err == nil, c.passes)
			}
		})
	}
}
