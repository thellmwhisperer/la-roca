package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/thellmwhisperer/la-roca-vector/internal/engine"
)

type Manifest struct {
	ID     string
	SHA256 string
	Bytes  int64
	URL    string
}

func DefaultManifest() Manifest {
	return Manifest{ID: ID, SHA256: SHA256, Bytes: Bytes, URL: DownloadURL}
}

func FilePath(dataDir string, manifest Manifest) string {
	return filepath.Join(dataDir, "models", manifest.ID, manifest.SHA256+".gguf")
}

func Existing(dataDir string, manifest Manifest) (string, error) {
	if err := validateManifest(manifest); err != nil {
		return "", err
	}
	path := FilePath(dataDir, manifest)
	if !validModelFile(path, manifest) {
		return "", fmt.Errorf("the embedding model is not downloaded")
	}
	return path, nil
}

func Ensure(ctx context.Context, dataDir string, manifest Manifest, sink engine.Sink) (string, error) {
	if err := validateManifest(manifest); err != nil {
		return "", err
	}
	path := FilePath(dataDir, manifest)
	if validModelFile(path, manifest) {
		emit(sink, engine.Result("download", "embedding model: ready"))
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create the embedding model directory: %w", err)
	}
	release, err := lockModel(path + ".lock")
	if err != nil {
		return "", fmt.Errorf("lock the embedding model download: %w", err)
	}
	defer release()
	if validModelFile(path, manifest) {
		emit(sink, engine.Result("download", "embedding model: ready"))
		return path, nil
	}
	_ = os.Remove(path)
	partial := path + ".partial"
	var verifyErr error
	for attempt := 0; attempt < 2; attempt++ {
		if err := download(ctx, manifest, partial, sink); err != nil {
			_ = os.Remove(partial)
			return "", err
		}
		verifyErr = verifyFile(partial, manifest.SHA256, manifest.Bytes)
		if verifyErr == nil {
			break
		}
		_ = os.Remove(partial)
	}
	if verifyErr != nil {
		return "", verifyErr
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return "", fmt.Errorf("install the embedding model: %w", err)
	}
	emit(sink, engine.Result("download", "embedding model: ready"))
	return path, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.ID == "" || manifest.SHA256 == "" || manifest.URL == "" || manifest.Bytes <= 0 {
		return fmt.Errorf("embedding model manifest is incomplete")
	}
	return nil
}

func validModelFile(path string, manifest Manifest) bool {
	current, err := os.Stat(path)
	return err == nil && current.Size() == manifest.Bytes &&
		verifyFile(path, manifest.SHA256, manifest.Bytes) == nil
}

func download(ctx context.Context, manifest Manifest, partial string, sink engine.Sink) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.URL, nil)
	if err != nil {
		return fmt.Errorf("download the embedding model: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download the embedding model: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download the embedding model: the source answered %s", response.Status)
	}
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("write the embedding model: %w", err)
	}
	defer file.Close()
	total := manifest.Bytes
	if response.ContentLength > 0 {
		total = response.ContentLength
	}
	started := time.Now()
	var written int64
	buffer := make([]byte, 256<<10)
	lastEmit := time.Time{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, err := file.Write(buffer[:n]); err != nil {
				return fmt.Errorf("write the embedding model: %w", err)
			}
			written += int64(n)
			if time.Since(lastEmit) >= 200*time.Millisecond || written == total {
				eta := time.Duration(0)
				if written > 0 {
					elapsed := time.Since(started)
					eta = time.Duration(float64(elapsed) * float64(total-written) / float64(written))
				}
				percent := engine.Percent(written, total)
				emit(sink, engine.Progress("download",
					fmt.Sprintf("downloading the embedding model · %d%% · %s of %s",
						percent, engine.FormatBytes(written), engine.FormatBytes(total)),
					written, total, eta))
				lastEmit = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("download the embedding model: %w", readErr)
		}
	}
	return file.Close()
}

func verifyFile(path, wantSHA string, wantBytes int64) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read the embedding model: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() != wantBytes {
		return fmt.Errorf("the embedding model is %d bytes, want %d", info.Size(), wantBytes)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return fmt.Errorf("hash the embedding model: %w", err)
	}
	got := hex.EncodeToString(digest.Sum(nil))
	if got != wantSHA {
		return fmt.Errorf("the embedding model did not match its pinned checksum")
	}
	return nil
}

func emit(sink engine.Sink, event engine.Event) {
	if sink != nil {
		sink(event)
	}
}
