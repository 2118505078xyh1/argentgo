package cli

import (
	"archive/zip"
	"bytes"
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
