package update

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// maxArchiveEntries caps how many entries we inspect in a downloaded zip.
	// A release archive only carries a couple of binaries.
	maxArchiveEntries = 256
	// maxExtractedEntry caps a single uncompressed entry (512 MiB).
	maxExtractedEntry = 512 << 20
	// maxExtractedTotal caps the sum of all extracted bytes (1 GiB), so a
	// compressed bomb cannot inflate past the download cap.
	maxExtractedTotal = 1 << 30
)

// executableNames are the only zip entries eligible for installation. The
// release archive also ships argent-native.exe, which must never replace the
// running CLI binary.
var executableNames = map[string]bool{
	"argent":     true,
	"argent.exe": true,
}

// isZipAsset reports whether the asset name denotes a zip archive.
func isZipAsset(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".zip")
}

// extractExecutable extracts the whitelisted argent executable from a zip
// archive into dir and returns the path of the extracted file.
//
// Extraction is deliberately restricted: absolute paths, ".." traversal,
// symlinks and other non-regular entries are rejected; entry counts and byte
// totals are capped; and only argent/argent.exe are ever written.
func extractExecutable(archive, dir string) (string, error) {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	defer reader.Close()

	if len(reader.File) == 0 {
		return "", errors.New("zip archive has no entries")
	}
	if len(reader.File) > maxArchiveEntries {
		return "", fmt.Errorf("zip archive has %d entries, over the %d limit",
			len(reader.File), maxArchiveEntries)
	}

	var chosen *zip.File
	var chosenName string
	var total int64
	for _, entry := range reader.File {
		name, safe := safeArchivePath(entry.Name)
		if !safe {
			return "", fmt.Errorf("unsafe zip entry %q", entry.Name)
		}
		mode := entry.Mode()
		if mode&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink zip entry %q", entry.Name)
		}
		if mode.IsDir() {
			continue
		}
		if mode&os.ModeType != 0 {
			return "", fmt.Errorf("unsupported zip entry %q", entry.Name)
		}
		base := path.Base(name)
		if !executableNames[strings.ToLower(base)] {
			continue
		}
		if entry.UncompressedSize64 > uint64(maxExtractedEntry) {
			return "", fmt.Errorf("zip entry %q is %d bytes, over the %d byte limit",
				entry.Name, entry.UncompressedSize64, maxExtractedEntry)
		}
		total += int64(entry.UncompressedSize64)
		if total > maxExtractedTotal {
			return "", fmt.Errorf("zip archive exceeds the %d byte extraction limit", maxExtractedTotal)
		}
		if chosen != nil {
			return "", errors.New("zip archive contains more than one argent executable")
		}
		chosen, chosenName = entry, base
	}
	if chosen == nil {
		return "", errors.New("zip archive has no argent or argent.exe entry")
	}

	// chosenName is a plain base name from safeArchivePath, so it cannot escape
	// the extraction directory.
	dest := filepath.Join(dir, chosenName)
	source, err := chosen.Open()
	if err != nil {
		return "", fmt.Errorf("open zip entry %q: %w", chosen.Name, err)
	}
	defer source.Close()
	output, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", dest, err)
	}
	written, copyErr := io.Copy(output, io.LimitReader(source, maxExtractedEntry+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return "", fmt.Errorf("extract %q: %w", chosen.Name, copyErr)
	}
	if written > maxExtractedEntry {
		_ = os.Remove(dest)
		return "", fmt.Errorf("zip entry %q exceeds the %d byte limit", chosen.Name, maxExtractedEntry)
	}
	if syncErr != nil {
		_ = os.Remove(dest)
		return "", fmt.Errorf("flush %s: %w", dest, syncErr)
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return "", fmt.Errorf("close %s: %w", dest, closeErr)
	}
	return dest, nil
}

// safeArchivePath validates an archive entry name, normalising Windows
// separators, and returns its cleaned, slash-separated path. It rejects
// absolute paths, drive letters, and any path that escapes its parent.
func safeArchivePath(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false
	}
	normalised := strings.ReplaceAll(name, `\`, `/`)
	if strings.HasPrefix(normalised, "/") {
		return "", false
	}
	if len(normalised) >= 2 && normalised[1] == ':' &&
		((normalised[0] >= 'a' && normalised[0] <= 'z') ||
			(normalised[0] >= 'A' && normalised[0] <= 'Z')) {
		return "", false
	}
	cleaned := path.Clean(normalised)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return cleaned, true
}
