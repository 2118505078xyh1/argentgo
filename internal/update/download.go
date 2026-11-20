package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxAssetSize caps a release download (512 MiB) so a hostile or broken
// response cannot fill the disk.
const MaxAssetSize int64 = 512 << 20

// downloadTimeout bounds a self-update download when the caller's context has
// no deadline of its own. Check uses a 30-second client timeout; downloads need
// the same protection, otherwise a server that accepts the connection but never
// sends (or stops mid-body) leaves the updater hanging forever. It is a variable
// so tests can shrink it without waiting; production always uses 30 seconds.
var downloadTimeout = 30 * time.Second

// withDownloadDeadline gives ctx a deadline if it does not already have one.
// An existing, possibly shorter, caller deadline is always preserved.
func withDownloadDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, downloadTimeout)
}

// Progress is called after every chunk with the bytes written so far and the
// expected total (0 when the server did not report one).
type Progress func(done, total int64)

// Download fetches asset into dest and verifies its SHA-256 checksum before the
// file appears at dest. A checksum mismatch removes the partial download. For a
// zip or gzip-compressed tar asset, the verified archive is unpacked with
// restricted extraction and the whitelisted executable (argent/argent.exe) is
// placed at dest; a bare binary is written unchanged.
func Download(ctx context.Context, asset Asset, dest, checksum string) error {
	return DownloadProgress(ctx, asset, dest, checksum, nil)
}

// DownloadProgress is Download with a progress callback.
func DownloadProgress(ctx context.Context, asset Asset, dest, checksum string, progress Progress) error {
	ctx, cancel := withDownloadDeadline(ctx)
	defer cancel()

	want, err := normaliseChecksum(checksum)
	if err != nil {
		return err
	}
	if asset.URL == "" {
		return fmt.Errorf("update: asset %q has no download url", asset.Name)
	}
	if asset.Size > MaxAssetSize {
		return fmt.Errorf("update: asset %q is %d bytes, over the %d byte limit",
			asset.Name, asset.Size, MaxAssetSize)
	}
	directory := filepath.Dir(dest)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("update: create %s: %w", directory, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return fmt.Errorf("update: build the download request: %w", err)
	}
	request.Header.Set("User-Agent", userAgent)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("update: download %s: %w", asset.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("update: download %s returned HTTP %d", asset.Name, response.StatusCode)
	}
	total := response.ContentLength
	if total <= 0 {
		total = asset.Size
	}

	temp, err := os.CreateTemp(directory, ".download-*")
	if err != nil {
		return fmt.Errorf("update: create a temporary file in %s: %w", directory, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	hasher := sha256.New()
	writer := io.MultiWriter(temp, hasher)
	buffer := make([]byte, 64<<10)
	var done int64
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			done += int64(read)
			if done > MaxAssetSize {
				temp.Close()
				return fmt.Errorf("update: download of %s exceeds the %d byte limit",
					asset.Name, MaxAssetSize)
			}
			if _, writeErr := writer.Write(buffer[:read]); writeErr != nil {
				temp.Close()
				return fmt.Errorf("update: write %s: %w", tempName, writeErr)
			}
			if progress != nil {
				progress(done, total)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			temp.Close()
			return fmt.Errorf("update: download %s: %w", asset.Name, readErr)
		}
	}

	if got := hex.EncodeToString(hasher.Sum(nil)); got != want {
		temp.Close()
		return fmt.Errorf("update: checksum mismatch for %s: expected %s, got %s",
			asset.Name, want, got)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("update: flush %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("update: close %s: %w", tempName, err)
	}
	if isZipAsset(asset.Name) || isTarGzAsset(asset.Name) {
		// The published checksum covers the archive, so it has been verified
		// above. Extract the executable into a private staging directory and
		// place that at dest; installing the archive itself would brick the
		// binary. FindAsset prefers .tar.gz/.tgz on non-Windows platforms, so
		// those must be unpacked here too (not staged as a raw gzip file).
		extractDir, err := os.MkdirTemp(directory, ".extract-*")
		if err != nil {
			return fmt.Errorf("update: create an extraction directory in %s: %w", directory, err)
		}
		defer os.RemoveAll(extractDir)
		var binary string
		if isTarGzAsset(asset.Name) {
			binary, err = extractExecutableFromTarGz(tempName, extractDir)
		} else {
			binary, err = extractExecutable(tempName, extractDir)
		}
		if err != nil {
			return fmt.Errorf("update: extract %s: %w", asset.Name, err)
		}
		if err := os.Rename(binary, dest); err != nil {
			return fmt.Errorf("update: place %s: %w", dest, err)
		}
		return nil
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		return fmt.Errorf("update: mark %s executable: %w", tempName, err)
	}
	if err := os.Rename(tempName, dest); err != nil {
		return fmt.Errorf("update: place %s: %w", dest, err)
	}
	return nil
}

// normaliseChecksum accepts a bare hex digest or "sha256:<hex>".
func normaliseChecksum(checksum string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(checksum))
	value = strings.TrimPrefix(value, "sha256:")
	if value == "" {
		return "", errors.New("update: a checksum is required for downloads")
	}
	if len(value) != sha256.Size*2 {
		return "", fmt.Errorf("update: %q is not a SHA-256 checksum", checksum)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("update: %q is not a SHA-256 checksum", checksum)
	}
	return value, nil
}
