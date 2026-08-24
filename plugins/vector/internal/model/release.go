package model

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
)

const ChecksumsFileName = "checksums.txt"

type ReleaseArtifacts struct {
	AssetName     string
	LicenseName   string
	ChecksumsName string
}

type ReleaseSpec struct {
	AssetName   string
	Asset       Manifest
	LicenseName string
	License     Manifest
}

func DefaultReleaseSpec() ReleaseSpec {
	return ReleaseSpec{
		AssetName:   FileName,
		Asset:       SourceManifest(),
		LicenseName: LicenseFileName,
		License: Manifest{
			ID: "apache-2.0", SHA256: LicenseSHA256, Bytes: LicenseBytes, URL: LicenseURL,
		},
	}
}

// StageRelease downloads through the normal verified model path, then exposes
// only the release asset, its license and their checksums in outputDir. The
// temporary hashed cache lives under outputDir and is withdrawn before return.
func StageRelease(ctx context.Context, outputDir string, spec ReleaseSpec,
	sink engine.Sink) (ReleaseArtifacts, error) {
	for _, name := range []string{spec.AssetName, spec.LicenseName} {
		if name == "" || filepath.Base(name) != name {
			return ReleaseArtifacts{}, fmt.Errorf("model release asset name is invalid")
		}
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return ReleaseArtifacts{}, fmt.Errorf("create model release directory: %w", err)
	}
	staging, err := os.MkdirTemp(outputDir, ".model-release-")
	if err != nil {
		return ReleaseArtifacts{}, fmt.Errorf("create model release staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	verifiedAsset, err := Ensure(ctx, staging, spec.Asset, sink)
	if err != nil {
		return ReleaseArtifacts{}, err
	}
	verifiedLicense, err := Ensure(ctx, staging, spec.License, nil)
	if err != nil {
		return ReleaseArtifacts{}, err
	}
	asset := filepath.Join(outputDir, spec.AssetName)
	if err := os.Rename(verifiedAsset, asset); err != nil {
		return ReleaseArtifacts{}, fmt.Errorf("stage model release asset: %w", err)
	}
	license := filepath.Join(outputDir, spec.LicenseName)
	if err := os.Rename(verifiedLicense, license); err != nil {
		_ = os.Remove(asset)
		return ReleaseArtifacts{}, fmt.Errorf("stage model release license: %w", err)
	}
	checksums := filepath.Join(outputDir, ChecksumsFileName)
	lines := spec.Asset.SHA256 + "  " + spec.AssetName + "\n" +
		spec.License.SHA256 + "  " + spec.LicenseName + "\n"
	if err := os.WriteFile(checksums, []byte(lines), 0o644); err != nil {
		_ = os.Remove(asset)
		_ = os.Remove(license)
		return ReleaseArtifacts{}, fmt.Errorf("write model release checksums: %w", err)
	}
	return ReleaseArtifacts{
		AssetName: spec.AssetName, LicenseName: spec.LicenseName, ChecksumsName: ChecksumsFileName,
	}, nil
}

func ValidateReleaseTag(tag string) error {
	if strings.TrimSpace(tag) != tag || tag != ReleaseTag {
		return fmt.Errorf("model release tag is %q, want %q", tag, ReleaseTag)
	}
	return nil
}
