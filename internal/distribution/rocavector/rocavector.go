// Package rocavector owns the executable vector plugin bundled into each Roca
// release binary.
package rocavector

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/plugininstall"
	"github.com/thellmwhisperer/la-roca/internal/provider/plugin"
)

const (
	Name          = "roca-vector"
	LegacyName    = "vector"
	StateDir      = "state"
	BundledSource = plugin.BundledSource
	trailerSize   = 16 + 8 + sha256.Size
)

var (
	trailerMagic = [16]byte{'R', 'O', 'C', 'A', '_', 'V', 'E', 'C', 'T', 'O', 'R', '_', 'V', '1'}
	manifest     = []byte(`{"schema":1,"name":"roca-vector","version":"dev","kind":"executable","state_directory":"state"}`)
)

func Ensure(root, binDir, version string) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version, BundleSpec())
}

func EnsureWithPayload(root, binDir, version string, payload []byte) (plugininstall.Result, error) {
	return bundledplugin.Ensure(root, binDir, version,
		bundleSpec(func() ([]byte, error) { return payload, nil }))
}

func BundleSpec() bundledplugin.Spec {
	return bundleSpec(Payload)
}

func bundleSpec(payload func() ([]byte, error)) bundledplugin.Spec {
	return bundledplugin.Spec{
		Name: Name, LegacyName: LegacyName, Executable: executableFilename(),
		Source: BundledSource, Manifest: manifest, Payload: payload,
	}
}

func executableFilename() string {
	if runtime.GOOS == "windows" {
		return "roca-vector.exe"
	}
	return "roca-vector"
}

// Payload reads and verifies the vector executable appended to the running Roca
// binary by the release build.
func Payload() ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate running roca binary: %w", err)
	}
	return ReadPayload(executable)
}

func ReadPayload(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read roca binary: %w", err)
	}
	start, length, expected, ok := payloadEnvelope(raw)
	if !ok {
		return nil, fmt.Errorf("running roca binary does not carry a bundled vector executable")
	}
	payload := raw[start : start+length]
	digest := sha256.Sum256(payload)
	if !bytes.Equal(digest[:], expected) {
		return nil, fmt.Errorf("bundled vector executable checksum mismatch")
	}
	return payload, nil
}

// AppendPayload adds one verified payload envelope to a freshly built core
// binary. If the destination already has an envelope, it is replaced.
func AppendPayload(binaryPath, payloadPath string) error {
	core, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read core binary: %w", err)
	}
	if start, _, _, ok := payloadEnvelope(core); ok {
		core = core[:start]
	}
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		return fmt.Errorf("read vector executable: %w", err)
	}
	if len(payload) == 0 {
		return fmt.Errorf("vector executable is empty")
	}
	digest := sha256.Sum256(payload)
	trailer := make([]byte, trailerSize)
	copy(trailer, trailerMagic[:])
	binary.LittleEndian.PutUint64(trailer[16:24], uint64(len(payload)))
	copy(trailer[24:], digest[:])
	combined := make([]byte, 0, len(core)+len(payload)+len(trailer))
	combined = append(combined, core...)
	combined = append(combined, payload...)
	combined = append(combined, trailer...)
	info, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("inspect core binary: %w", err)
	}
	if err := os.WriteFile(binaryPath, combined, info.Mode().Perm()); err != nil {
		return fmt.Errorf("write bundled core binary: %w", err)
	}
	return nil
}

func payloadEnvelope(raw []byte) (start, length int, digest []byte, ok bool) {
	if len(raw) < trailerSize {
		return 0, 0, nil, false
	}
	trailer := raw[len(raw)-trailerSize:]
	if !bytes.Equal(trailer[:16], trailerMagic[:]) {
		return 0, 0, nil, false
	}
	size := binary.LittleEndian.Uint64(trailer[16:24])
	if size == 0 || size > uint64(len(raw)-trailerSize) {
		return 0, 0, nil, false
	}
	length = int(size)
	start = len(raw) - trailerSize - length
	return start, length, trailer[24:], true
}
