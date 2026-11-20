package cli

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectArchiveRefusesARawZipButAcceptsABinary(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	item, err := writer.Create("argent.exe")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := item.Write([]byte("MZ payload")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "archive.zip")
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := rejectArchive(archivePath); err == nil || !strings.Contains(err.Error(), "zip archive") {
		t.Fatalf("rejectArchive(zip) = %v, want a refusal", err)
	}

	binaryPath := filepath.Join(dir, "argent.exe")
	if err := os.WriteFile(binaryPath, []byte("MZ payload"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := rejectArchive(binaryPath); err != nil {
		t.Fatalf("rejectArchive(binary): %v", err)
	}
}

func TestVerifyStagedBinarySkipsArchiveHashForZips(t *testing.T) {
	dir := t.TempDir()
	extracted := filepath.Join(dir, "argent.exe")
	if err := os.WriteFile(extracted, []byte("MZ extracted"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// The checksum is of the zip, not the extracted exe. For a zip asset the
	// re-check must not compare the extracted bytes against that checksum.
	if err := verifyStagedBinary(extracted, "argent_1.0.0_windows_amd64.zip", strings.Repeat("0", 64)); err != nil {
		t.Fatalf("verifyStagedBinary(zip): %v", err)
	}
	if err := verifyStagedBinary(extracted, "argent_windows_amd64.exe", strings.Repeat("0", 64)); err == nil {
		t.Fatal("verifyStagedBinary accepted a bare exe with a wrong checksum")
	}
}

func TestVerifyStagedBinarySkipsArchiveHashForTarGz(t *testing.T) {
	dir := t.TempDir()
	// A tar.gz/tgz download stages the extracted ELF, not the compressed
	// archive, so the published checksum (of the archive) must not be compared
	// against these bytes.
	extracted := filepath.Join(dir, "argent")
	if err := os.WriteFile(extracted, []byte("\x7fELF extracted binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	for _, name := range []string{
		"argent_1.0.0_linux_amd64.tar.gz",
		"argent_1.0.0_linux_amd64.tgz",
		"ARGENT_1.0.0_LINUX_AMD64.TAR.GZ",
	} {
		if err := verifyStagedBinary(extracted, name, strings.Repeat("0", 64)); err != nil {
			t.Errorf("verifyStagedBinary(%q): %v", name, err)
		}
	}
	// A bare binary still gets re-hashed and must reject a wrong checksum.
	if err := verifyStagedBinary(extracted, "argent_linux_amd64", strings.Repeat("0", 64)); err == nil {
		t.Fatal("verifyStagedBinary accepted a bare binary with a wrong checksum")
	}
}

func TestRejectArchiveRefusesRawGzipButAcceptsExtractedBinary(t *testing.T) {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write([]byte("not the executable we want to install")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "argent.tar.gz")
	if err := os.WriteFile(archivePath, buffer.Bytes(), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := rejectArchive(archivePath); err == nil || !strings.Contains(err.Error(), "gzip archive") {
		t.Fatalf("rejectArchive(raw gzip) = %v, want a refusal", err)
	}
	// 同样的防护也通过 verifyStagedBinary 作用于某个 tgz 资源：其暂存文件仍是原始 gzip
	// （尚未完成解压）。
	if err := verifyStagedBinary(archivePath, "argent_linux_amd64.tgz", strings.Repeat("0", 64)); err == nil {
		t.Fatal("verifyStagedBinary accepted a raw gzip as the staged executable")
	}

	binaryPath := filepath.Join(dir, "argent")
	if err := os.WriteFile(binaryPath, []byte("\x7fELF extracted binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := rejectArchive(binaryPath); err != nil {
		t.Fatalf("rejectArchive(extracted binary): %v", err)
	}
}

func TestIsArchiveAssetName(t *testing.T) {
	cases := map[string]bool{
		"argent.zip":         true,
		"argent.ZIP":         true,
		"argent.tar.gz":      true,
		"argent.tgz":         true,
		"ARGENT.TAR.GZ":      true,
		"argent_linux_amd64": false,
		"argent.exe":         false,
		"argent.tar":         false,
		"argent.gz":          false,
	}
	for name, want := range cases {
		if got := isArchiveAssetName(name); got != want {
			t.Errorf("isArchiveAssetName(%q) = %v, want %v", name, got, want)
		}
	}
}
