package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func main() {
	binary := flag.String("binary", "", "roca-vector binary to package")
	out := flag.String("out", "", "package output directory")
	version := flag.String("version", "dev", "package version")
	targetOS := flag.String("target-os", runtime.GOOS, "target operating system")
	flag.Parse()
	if err := packagePlugin(*binary, *out, *version, *targetOS); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func packagePlugin(binary, out, version, targetOS string) error {
	if strings.TrimSpace(binary) == "" || strings.TrimSpace(out) == "" ||
		strings.TrimSpace(version) == "" || strings.TrimSpace(targetOS) == "" {
		return fmt.Errorf("binary, out, version, and target OS are required")
	}
	name := "roca-vector"
	if targetOS == "windows" {
		name += ".exe"
	}
	if err := prepareOutput(out, name); err != nil {
		return err
	}
	if err := copyFile(binary, filepath.Join(out, name), 0o700); err != nil {
		return err
	}
	metadata, err := json.MarshalIndent(map[string]any{
		"schema": 1, "name": "roca-vector", "version": version,
		"kind": "executable", "state_directory": "state",
	}, "", "  ")
	if err != nil {
		return err
	}
	metadata = append(metadata, '\n')
	if err := os.WriteFile(filepath.Join(out, "plugin.json"), metadata, 0o600); err != nil {
		return fmt.Errorf("write plugin.json: %w", err)
	}
	files := []string{"plugin.json", name}
	sort.Strings(files)
	var checksums strings.Builder
	for _, file := range files {
		raw, err := os.ReadFile(filepath.Join(out, file))
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		fmt.Fprintf(&checksums, "%s  %s\n", hex.EncodeToString(digest[:]), file)
	}
	if err := os.WriteFile(filepath.Join(out, "checksums.txt"), []byte(checksums.String()), 0o600); err != nil {
		return fmt.Errorf("write checksums.txt: %w", err)
	}
	return nil
}

func prepareOutput(out, executable string) error {
	absolute, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("resolve package output: %w", err)
	}
	if filepath.Dir(absolute) == absolute {
		return fmt.Errorf("package output may not be a filesystem root")
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return fmt.Errorf("create package output: %w", err)
	}
	allowed := map[string]bool{"plugin.json": true, "checksums.txt": true, executable: true}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return fmt.Errorf("inspect package output: %w", err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !allowed[entry.Name()] || !info.Mode().IsRegular() {
			return fmt.Errorf("package output contains an unmanaged entry %s", filepath.Join(absolute, entry.Name()))
		}
		if err := os.Remove(filepath.Join(absolute, entry.Name())); err != nil {
			return fmt.Errorf("clear packaged file %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open binary: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create packaged binary: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy binary: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close packaged binary: %w", err)
	}
	return nil
}
