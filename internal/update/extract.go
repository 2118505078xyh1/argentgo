package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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

// isTarGzAsset reports whether the asset name denotes a gzip-compressed tar
// archive. FindAsset gives .tar.gz/.tgz the highest extension bonus on non-Windows
// platforms, so Download must unpack them just like zip archives or it would
// stage a raw archive and brick the binary.
func isTarGzAsset(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")
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

// extractExecutableFromTarGz extracts the whitelisted argent executable from a
// gzip-compressed tar archive into dir and returns the path of the extracted
// file. It applies the same restrictions as extractExecutable: path and symlink
// checks, entry/byte caps, and a single whitelisted executable. The decompressed
// stream is bounded as a whole so a compression bomb cannot inflate past the
// per-entry caps regardless of what the tar headers claim.
func extractExecutableFromTarGz(archive, dir string) (string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return "", fmt.Errorf("open tar.gz: %w", err)
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gzipReader.Close()

	// tar.Reader is a forward-only stream, so bound the total decompressed bytes
	// rather than trusting individual header sizes when skipping entries.
	tarReader := tar.NewReader(io.LimitReader(gzipReader, maxExtractedTotal+1))

	var dest string
	var total int64
	entries := 0
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = os.Remove(dest)
			return "", fmt.Errorf("read tar: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			_ = os.Remove(dest)
			return "", fmt.Errorf("tar archive has more than %d entries", maxArchiveEntries)
		}
		name, safe := safeArchivePath(header.Name)
		if !safe {
			_ = os.Remove(dest)
			return "", fmt.Errorf("unsafe tar entry %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
			// A regular file; checked against the whitelist below.
		default:
			// Symlinks, hardlinks, devices, FIFOs, etc. must never be installed.
			_ = os.Remove(dest)
			return "", fmt.Errorf("unsupported tar entry %q", header.Name)
		}
		base := path.Base(name)
		if !executableNames[strings.ToLower(base)] {
			continue
		}
		if dest != "" {
			_ = os.Remove(dest)
			return "", errors.New("tar archive contains more than one argent executable")
		}
		if header.Size < 0 || header.Size > maxExtractedEntry {
			_ = os.Remove(dest)
			return "", fmt.Errorf("tar entry %q is %d bytes, over the %d byte limit",
				header.Name, header.Size, maxExtractedEntry)
		}
		total += header.Size
		if total > maxExtractedTotal {
			_ = os.Remove(dest)
			return "", fmt.Errorf("tar archive exceeds the %d byte extraction limit", maxExtractedTotal)
		}

		// base is a plain name returned by safeArchivePath, so it cannot escape
		// the extraction directory.
		dest = filepath.Join(dir, base)
		output, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
		if err != nil {
			dest = ""
			return "", fmt.Errorf("create %s: %w", filepath.Join(dir, base), err)
		}
		written, copyErr := io.Copy(output, io.LimitReader(tarReader, maxExtractedEntry+1))
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil {
			_ = os.Remove(dest)
			return "", fmt.Errorf("extract %q: %w", header.Name, copyErr)
		}
		if written > maxExtractedEntry {
			_ = os.Remove(dest)
			return "", fmt.Errorf("tar entry %q exceeds the %d byte limit", header.Name, maxExtractedEntry)
		}
		if syncErr != nil {
			_ = os.Remove(dest)
			return "", fmt.Errorf("flush %s: %w", dest, syncErr)
		}
		if closeErr != nil {
			_ = os.Remove(dest)
			return "", fmt.Errorf("close %s: %w", dest, closeErr)
		}
	}
	if dest == "" {
		return "", errors.New("tar archive has no argent or argent.exe entry")
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
