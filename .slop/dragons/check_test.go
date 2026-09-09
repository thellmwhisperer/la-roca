package dragons

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type record struct {
	ID     string                                     `yaml:"id"`
	Status string                                     `yaml:"status"`
	Forbid struct{ Paths, Symbols, Strings []string } `yaml:"forbid"`
}

// All versioned sources and fixtures, regardless of extension. The register
// and narrative/evidence documents are outside these declared product roots.
var scope = []string{"cmd", "internal", "data", "pkg", "plugins", "scripts", "test", "testdata", "features", ".github", "Makefile", "go.mod", "go.sum", "install.sh", "plugin.json", "mcp.json"}

func check(root string) error {
	files, err := filepath.Glob(filepath.Join(root, ".slop/dragons/*.yaml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("missing dragon records")
	}
	cmd := exec.Command("git", append([]string{"ls-files", "-z", "--"}, scope...)...)
	cmd.Dir = root
	tracked, err := cmd.Output()
	if err != nil {
		return err
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var dragon record
		if err := yaml.Unmarshal(body, &dragon); err != nil {
			return err
		}
		if dragon.Status != "removed" {
			continue
		}
		needles := append(append([]string{}, dragon.Forbid.Symbols...), dragon.Forbid.Strings...)
		entries := append(append([]string{}, dragon.Forbid.Paths...), needles...)
		if len(entries) == 0 {
			return fmt.Errorf("%s: removed record has empty forbid", dragon.ID)
		}
		for _, value := range entries {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s: empty forbid entry", dragon.ID)
			}
		}
		for _, name := range strings.Split(string(tracked), "\x00") {
			if name == "" {
				continue
			}
			for _, pattern := range dragon.Forbid.Paths {
				match, err := filepath.Match(pattern, name)
				if err != nil {
					return err
				}
				if strings.HasSuffix(pattern, "/**") {
					match = strings.HasPrefix(name, strings.TrimSuffix(pattern, "**"))
				}
				if match {
					return fmt.Errorf("%s: forbidden path %s", dragon.ID, name)
				}
			}
			body, err := os.ReadFile(filepath.Join(root, name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			for _, needle := range needles {
				// A scoped string uses prefix**suffix::text; ** crosses directories.
				if pattern, text, scoped := strings.Cut(needle, "::"); scoped {
					prefix, suffix, wildcard := strings.Cut(pattern, "**")
					if !wildcard {
						return fmt.Errorf("%s: scoped string requires **", dragon.ID)
					}
					if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
						continue
					}
					if text == "" {
						return fmt.Errorf("%s: empty scoped string", dragon.ID)
					}
					needle = text
				}
				if bytes.Contains(body, []byte(needle)) {
					return fmt.Errorf("%s: forbidden content in %s", dragon.ID, name)
				}
			}
		}
	}
	return nil
}

func TestRemovedDragons(t *testing.T) {
	if err := check(filepath.Join("..", "..")); err != nil {
		t.Fatal(err)
	}
}

func TestGateScopeAndRemovedForbid(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", name)
	}
	git("init", "-q")
	recordPath := ".slop/dragons/probe.yaml"
	declaration := "id: probe\nstatus: removed\nforbid:\n  strings: [\"retired\\u002doperation\"]\n"
	write(recordPath, declaration)
	write("INFORME-SLOP-ADYACENTE-2.json", `{"evidence":"retired-operation"}`)
	if err := check(root); err != nil {
		t.Fatalf("evidence is not code: %v", err)
	}
	for _, name := range []string{"Makefile", "scripts/dragons/extensionless", "testdata/probe.json"} {
		write(name, "retired-operation")
		if err := check(root); err == nil {
			t.Fatalf("reintroduced operation in %s passed", name)
		}
		write(name, "clean")
	}
	write(recordPath, "id: probe\nstatus: removed\nforbid: {}\n")
	if err := check(root); err == nil {
		t.Fatal("empty removed forbid passed")
	}
	write(recordPath, declaration)
	if err := check(root); err != nil {
		t.Fatal(err)
	}
	// Exercise the actual retired S3 package forbid as a versioned source.
	raw, err := os.ReadFile("S3-terminal-observer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var s3 record
	if err := yaml.Unmarshal(raw, &s3); err != nil {
		t.Fatal(err)
	}
	write(recordPath, string(raw))
	forbidden := strings.TrimSuffix(s3.Forbid.Paths[0], "**") + "probe.go"
	write(forbidden, "package probe")
	if err := check(root); err == nil {
		t.Fatal("S3 package reintroduction passed")
	}
}

func TestScopedPublishedBinaryForbid(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	raw, err := os.ReadFile("D1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".slop/dragons"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".slop/dragons/D1.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var d1 record
	if err := yaml.Unmarshal(raw, &d1); err != nil {
		t.Fatal(err)
	}
	for _, entry := range d1.Forbid.Strings {
		_, text, scoped := strings.Cut(entry, "::")
		if !scoped {
			t.Fatal("D1 published lookup forbid must be scoped")
		}
		for _, name := range []string{"internal/probe.go", "internal/nested/probe_test.go"} {
			file := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("git", "add", name)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %v %s", err, out)
			}
			if err := check(root); (err != nil) != strings.HasSuffix(name, "_test.go") {
				t.Fatalf("scope %s: %v", name, err)
			}
			if err := os.WriteFile(file, nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
}
