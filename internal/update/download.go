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
)

// MaxAssetSize caps a release download (512 MiB) so a hostile or broken
// response cannot fill the disk.
const MaxAssetSize int64 = 512 << 20

// Progress is called after every chunk with the bytes written so far and the
// expected total (0 when the server did not report one).
type Progress func(done, total int64)

// Download fetches asset into dest and verifies its SHA-256 checksum before the
// file appears at dest. A checksum mismatch removes the partial download.
func Download(ctx context.Context, asset Asset, dest, checksum string) error {
	return DownloadProgress(ctx, asset, dest, checksum, nil)
}

// DownloadProgress is Download with a progress callback.
func DownloadProgress(ctx context.Context, asset Asset, dest, checksum string, progress Progress) error {
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
