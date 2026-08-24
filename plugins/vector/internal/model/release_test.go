package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageReleasePublishesOnlyVerifiedAssetAndChecksum(t *testing.T) {
	payload := []byte("synthetic model release bytes")
	digest := sha256.Sum256(payload)
	license := []byte("synthetic Apache license")
	licenseDigest := sha256.Sum256(license)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/license" {
			_, _ = response.Write(license)
			return
		}
		_, _ = response.Write(payload)
	}))
	t.Cleanup(server.Close)

	output := t.TempDir()
	artifacts, err := StageRelease(context.Background(), output, ReleaseSpec{
		AssetName: "model.gguf",
		Asset: Manifest{ID: "synthetic-model", SHA256: hex.EncodeToString(digest[:]),
			Bytes: int64(len(payload)), URL: server.URL + "/model"},
		LicenseName: "LICENSE-model.txt",
		License: Manifest{ID: "synthetic-license", SHA256: hex.EncodeToString(licenseDigest[:]),
			Bytes: int64(len(license)), URL: server.URL + "/license"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.AssetName != "model.gguf" || artifacts.LicenseName != "LICENSE-model.txt" ||
		artifacts.ChecksumsName != ChecksumsFileName {
		t.Fatalf("release artifacts = %+v", artifacts)
	}
	stored, err := os.ReadFile(filepath.Join(output, artifacts.AssetName))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(payload) {
		t.Fatalf("release asset = %q", stored)
	}
	storedLicense, err := os.ReadFile(filepath.Join(output, artifacts.LicenseName))
	if err != nil {
		t.Fatal(err)
	}
	if string(storedLicense) != string(license) {
		t.Fatalf("release license = %q", storedLicense)
	}
	checksums, err := os.ReadFile(filepath.Join(output, artifacts.ChecksumsName))
	if err != nil {
		t.Fatal(err)
	}
	wantChecksum := hex.EncodeToString(digest[:]) + "  model.gguf\n" +
		hex.EncodeToString(licenseDigest[:]) + "  LICENSE-model.txt\n"
	if string(checksums) != wantChecksum {
		t.Fatalf("checksums = %q, want %q", checksums, wantChecksum)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("release directory kept staging state: %v", entries)
	}
}

func TestStageReleaseRefusesUnverifiedBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("wrong bytes"))
	}))
	t.Cleanup(server.Close)

	output := t.TempDir()
	_, err := StageRelease(context.Background(), output, ReleaseSpec{
		AssetName: "model.gguf",
		Asset: Manifest{
			ID: "synthetic-model", SHA256: strings.Repeat("ab", 32), Bytes: 11, URL: server.URL,
		},
		LicenseName: "LICENSE-model.txt",
		License: Manifest{
			ID: "synthetic-license", SHA256: strings.Repeat("cd", 32), Bytes: 11, URL: server.URL,
		},
	}, nil)
	if err == nil {
		t.Fatal("unverified release bytes were accepted")
	}
	entries, readErr := os.ReadDir(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed release left artifacts: %v", entries)
	}
}

func TestModelReleaseTagIsExact(t *testing.T) {
	if err := ValidateReleaseTag(ReleaseTag); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"v1.0.0", "models-v2", " models-v1 "} {
		if err := ValidateReleaseTag(tag); err == nil {
			t.Fatalf("tag %q was accepted", tag)
		}
	}
}
