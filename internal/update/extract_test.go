package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

// zipFixture builds an in-memory zip archive from the given entries.
func zipFixture(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		item, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("CreateHeader(%q): %v", entry.name, err)
		}
		if _, err := item.Write(entry.data); err != nil {
			t.Fatalf("Write(%q): %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buffer.Bytes()
}

func serveBytes(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

// TestZipAssetIsExtractedBeforeApply is the end-to-end regression test: the
// published windows archive contains argent.exe, FindAsset picks it, and the
// installed binary must be the executable inside the zip, never the zip itself.
func TestZipAssetIsExtractedBeforeApply(t *testing.T) {
	binary := []byte("MZ fictitious executable payload")
	archive := zipFixture(t,
		zipEntry{name: "argent.exe", data: binary},
		zipEntry{name: "argent-native.exe", data: []byte("native frontend")},
	)
	server := serveBytes(t, archive)

	asset, found := FindAsset([]Asset{{
		Name: "argent_1.2.3_windows_amd64.zip", URL: server.URL, Size: int64(len(archive)),
	}}, "windows", "amd64")
	if !found {
		t.Fatal("FindAsset did not select the windows zip")
	}
	if !isZipAsset(asset.Name) {
		t.Fatalf("asset %q was not recognised as a zip", asset.Name)
	}

	dir := t.TempDir()
	stage := filepath.Join(dir, asset.Name)
	if err := Download(context.Background(), asset, stage, checksumOf(archive)); err != nil {
		t.Fatalf("Download: %v", err)
	}
	staged, err := os.ReadFile(stage)
	if err != nil {
		t.Fatalf("ReadFile(stage): %v", err)
	}
	if !bytes.Equal(staged, binary) {
		t.Fatalf("staged file = %q, want the extracted executable %q", staged, binary)
	}
	if bytes.HasPrefix(staged, []byte("PK")) {
		t.Fatal("staged file is still a zip archive")
	}

	target := filepath.Join(dir, "argent.exe")
	if err := os.WriteFile(target, []byte("old build"), 0o755); err != nil {
		t.Fatalf("WriteFile(target): %v", err)
	}
	if err := Apply(stage, target); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target): %v", err)
	}
	if !bytes.Equal(installed, binary) {
		t.Fatalf("installed binary = %q, want %q", installed, binary)
	}
	if bytes.HasPrefix(installed, []byte("PK")) {
		t.Fatal("Apply installed a zip archive as the executable")
	}
}

// TestDownloadRejectsUnsafeZipEntries covers zip-slip, absolute paths, Windows
// drive letters and symlinks. Each must fail before anything is staged.
func TestDownloadRejectsUnsafeZipEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
	}{
		{
			name: "parent traversal",
			entries: []zipEntry{
				{name: "../evil.exe", data: []byte("evil")},
				{name: "argent.exe", data: []byte("MZ good")},
			},
		},
		{
			name: "nested traversal",
			entries: []zipEntry{
				{name: "dir/../../evil.exe", data: []byte("evil")},
				{name: "argent.exe", data: []byte("MZ good")},
			},
		},
		{
			name: "absolute path",
			entries: []zipEntry{
				{name: "/evil.exe", data: []byte("evil")},
				{name: "argent.exe", data: []byte("MZ good")},
			},
		},
		{
			name: "windows drive letter",
			entries: []zipEntry{
				{name: "C:/evil.exe", data: []byte("evil")},
				{name: "argent.exe", data: []byte("MZ good")},
			},
		},
		{
			name: "windows absolute via backslash",
			entries: []zipEntry{
				{name: `\evil.exe`, data: []byte("evil")},
				{name: "argent.exe", data: []byte("MZ good")},
			},
		},
		{
			name: "symlink entry",
			entries: []zipEntry{
				{name: "argent.exe", data: []byte("../evil"), mode: os.ModeSymlink | 0o777},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			archive := zipFixture(t, test.entries...)
			server := serveBytes(t, archive)
			stage := filepath.Join(t.TempDir(), "argent.zip")
			err := Download(context.Background(),
				Asset{Name: "argent.zip", URL: server.URL, Size: int64(len(archive))},
				stage, checksumOf(archive))
			if err == nil {
				t.Fatal("Download accepted an unsafe zip entry")
			}
			if _, statErr := os.Stat(stage); !os.IsNotExist(statErr) {
				t.Fatalf("stage exists after a failed extraction: %v", statErr)
			}
		})
	}
}

func TestDownloadRejectsArchiveWithoutAnExecutable(t *testing.T) {
	archive := zipFixture(t,
		zipEntry{name: "argent-native.exe", data: []byte("native")},
		zipEntry{name: "README.md", data: []byte("docs")},
	)
	server := serveBytes(t, archive)
	stage := filepath.Join(t.TempDir(), "argent.zip")
	err := Download(context.Background(),
		Asset{Name: "argent.zip", URL: server.URL, Size: int64(len(archive))},
		stage, checksumOf(archive))
	if err == nil || !strings.Contains(err.Error(), "no argent") {
		t.Fatalf("error = %v, want a missing-executable error", err)
	}
}

func TestDownloadRejectsArchivesWithTooManyEntries(t *testing.T) {
	entries := make([]zipEntry, 0, maxArchiveEntries+2)
	for index := 0; index < maxArchiveEntries+1; index++ {
		entries = append(entries, zipEntry{name: "extra.bin", data: []byte("x")})
	}
	entries = append(entries, zipEntry{name: "argent.exe", data: []byte("MZ ok")})
	archive := zipFixture(t, entries...)
	server := serveBytes(t, archive)
	stage := filepath.Join(t.TempDir(), "argent.zip")
	err := Download(context.Background(),
		Asset{Name: "argent.zip", URL: server.URL, Size: int64(len(archive))},
		stage, checksumOf(archive))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v, want an entry-limit error", err)
	}
}

func TestApplyRefusesToInstallARawArchive(t *testing.T) {
	archive := zipFixture(t, zipEntry{name: "argent.exe", data: []byte("MZ")})
	dir := t.TempDir()
	stage := filepath.Join(dir, "argent.zip")
	if err := os.WriteFile(stage, archive, 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	target := filepath.Join(dir, "argent.exe")
	if err := os.WriteFile(target, []byte("old build"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := Apply(stage, target); err == nil || !strings.Contains(err.Error(), "zip archive") {
		t.Fatalf("error = %v, want a zip-archive refusal", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old build" {
		t.Fatalf("target = %q, err %v; it must be left untouched", got, err)
	}
}

func TestBareExecutableIsCompatible(t *testing.T) {
	binary := []byte("MZ bare executable")
	server := serveBytes(t, binary)
	asset, found := FindAsset([]Asset{{
		Name: "argent_windows_amd64.exe", URL: server.URL, Size: int64(len(binary)),
	}}, "windows", "amd64")
	if !found {
		t.Fatal("FindAsset did not select the bare exe")
	}
	if isZipAsset(asset.Name) {
		t.Fatalf("asset %q was wrongly recognized as a zip", asset.Name)
	}

	dir := t.TempDir()
	stage := filepath.Join(dir, asset.Name)
	if err := Download(context.Background(), asset, stage, checksumOf(binary)); err != nil {
		t.Fatalf("Download: %v", err)
	}
	staged, err := os.ReadFile(stage)
	if err != nil {
		t.Fatalf("ReadFile(stage): %v", err)
	}
	if !bytes.Equal(staged, binary) {
		t.Fatalf("staged file = %q, want %q", staged, binary)
	}
	target := filepath.Join(dir, "argent.exe")
	if err := Apply(stage, target); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(installed, binary) {
		t.Fatalf("installed = %q, err %v", installed, err)
	}
}

func TestSafeArchivePath(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{name: "argent.exe", want: "argent.exe", ok: true},
		{name: "dir/argent", want: "dir/argent", ok: true},
		{name: "./argent.exe", want: "argent.exe", ok: true},
		{name: `dir\argent.exe`, want: "dir/argent.exe", ok: true},
		{name: "../argent.exe", ok: false},
		{name: "dir/../../argent.exe", ok: false},
		{name: "/argent.exe", ok: false},
		{name: `C:\argent.exe`, ok: false},
		{name: "C:/argent.exe", ok: false},
		{name: "..", ok: false},
		{name: "", ok: false},
	}
	for _, test := range cases {
		got, ok := safeArchivePath(test.name)
		if ok != test.ok || (ok && got != test.want) {
			t.Errorf("safeArchivePath(%q) = (%q, %v), want (%q, %v)",
				test.name, got, ok, test.want, test.ok)
		}
	}
}

type tarEntry struct {
	name     string
	data     []byte
	typeflag byte
	linkname string
}

// tarGzFixture builds an in-memory gzip-compressed tar archive from the given
// entries. A zero typeflag means a regular file.
func tarGzFixture(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		flag := entry.typeflag
		if flag == 0 {
			flag = tar.TypeReg
		}
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o755,
			Typeflag: flag,
			Linkname: entry.linkname,
		}
		if flag == tar.TypeReg || flag == tar.TypeRegA {
			header.Size = int64(len(entry.data))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader(%q): %v", entry.name, err)
		}
		if flag == tar.TypeReg || flag == tar.TypeRegA {
			if _, err := tarWriter.Write(entry.data); err != nil {
				t.Fatalf("Write(%q): %v", entry.name, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buffer.Bytes()
}

// TestTarGzAssetIsExtractedBeforeApply is the regression for the cross-package
// gap: FindAsset prefers .tar.gz over .zip on Linux, so Download must unpack it
// rather than stage a raw gzip archive that Apply would install as the binary.
func TestTarGzAssetIsExtractedBeforeApply(t *testing.T) {
	binary := []byte("ELF fictitious executable payload")
	archive := tarGzFixture(t,
		tarEntry{name: "argent", data: binary},
		tarEntry{name: "argent-native", data: []byte("native frontend")},
	)
	server := serveBytes(t, archive)

	assets := []Asset{
		{Name: "argent_1.2.3_linux_amd64.zip", URL: "u1", Size: int64(len(archive))},
		{Name: "argent_1.2.3_linux_amd64.tar.gz", URL: server.URL, Size: int64(len(archive))},
	}
	asset, found := FindAsset(assets, "linux", "amd64")
	if !found || asset.Name != "argent_1.2.3_linux_amd64.tar.gz" {
		t.Fatalf("FindAsset = %+v, %v; want the .tar.gz asset", asset, found)
	}
	if !isTarGzAsset(asset.Name) {
		t.Fatalf("asset %q was not recognised as a tar.gz", asset.Name)
	}

	dir := t.TempDir()
	stage := filepath.Join(dir, asset.Name)
	if err := Download(context.Background(), asset, stage, checksumOf(archive)); err != nil {
		t.Fatalf("Download: %v", err)
	}
	staged, err := os.ReadFile(stage)
	if err != nil {
		t.Fatalf("ReadFile(stage): %v", err)
	}
	if !bytes.Equal(staged, binary) {
		t.Fatalf("staged file = %q, want the extracted executable %q", staged, binary)
	}
	if looksLikeGzip(stage) {
		t.Fatal("staged file is still a gzip archive")
	}

	target := filepath.Join(dir, "argent")
	if err := os.WriteFile(target, []byte("old build"), 0o755); err != nil {
		t.Fatalf("WriteFile(target): %v", err)
	}
	if err := Apply(stage, target); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target): %v", err)
	}
	if !bytes.Equal(installed, binary) {
		t.Fatalf("installed binary = %q, want %q", installed, binary)
	}
}

// TestDownloadRejectsUnsafeTarEntries covers path escapes, links and duplicate
// executables. Each must fail before anything is staged.
func TestDownloadRejectsUnsafeTarEntries(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
	}{
		{
			name: "parent traversal",
			entries: []tarEntry{
				{name: "../evil", data: []byte("evil")},
				{name: "argent", data: []byte("good")},
			},
		},
		{
			name: "nested traversal",
			entries: []tarEntry{
				{name: "dir/../../evil", data: []byte("evil")},
				{name: "argent", data: []byte("good")},
			},
		},
		{
			name: "absolute path",
			entries: []tarEntry{
				{name: "/evil", data: []byte("evil")},
				{name: "argent", data: []byte("good")},
			},
		},
		{
			name: "windows drive letter",
			entries: []tarEntry{
				{name: "C:/evil", data: []byte("evil")},
				{name: "argent", data: []byte("good")},
			},
		},
		{
			name: "symlink entry",
			entries: []tarEntry{
				{name: "argent", typeflag: tar.TypeSymlink, linkname: "../evil"},
			},
		},
		{
			name: "hardlink entry",
			entries: []tarEntry{
				{name: "argent", typeflag: tar.TypeLink, linkname: "elsewhere"},
			},
		},
		{
			name: "two executables",
			entries: []tarEntry{
				{name: "dir1/argent", data: []byte("one")},
				{name: "dir2/argent", data: []byte("two")},
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			archive := tarGzFixture(t, test.entries...)
			server := serveBytes(t, archive)
			stage := filepath.Join(t.TempDir(), "argent.tar.gz")
			err := Download(context.Background(),
				Asset{Name: "argent.tar.gz", URL: server.URL, Size: int64(len(archive))},
				stage, checksumOf(archive))
			if err == nil {
				t.Fatal("Download accepted an unsafe tar entry")
			}
			if _, statErr := os.Stat(stage); !os.IsNotExist(statErr) {
				t.Fatalf("stage exists after a failed extraction: %v", statErr)
			}
		})
	}
}

// TestDownloadRejectsTarGzWithoutAnExecutable also exercises the .tgz suffix.
func TestDownloadRejectsTarGzWithoutAnExecutable(t *testing.T) {
	archive := tarGzFixture(t,
		tarEntry{name: "README.md", data: []byte("docs")},
		tarEntry{name: "argent-native", data: []byte("native")},
	)
	server := serveBytes(t, archive)
	stage := filepath.Join(t.TempDir(), "argent.tgz")
	err := Download(context.Background(),
		Asset{Name: "argent_linux_amd64.tgz", URL: server.URL, Size: int64(len(archive))},
		stage, checksumOf(archive))
	if err == nil || !strings.Contains(err.Error(), "no argent") {
		t.Fatalf("error = %v, want a missing-executable error", err)
	}
}

func TestDownloadRejectsTarGzWithTooManyEntries(t *testing.T) {
	entries := make([]tarEntry, 0, maxArchiveEntries+2)
	for index := 0; index < maxArchiveEntries+1; index++ {
		entries = append(entries, tarEntry{name: fmt.Sprintf("extra%d.bin", index), data: []byte("x")})
	}
	entries = append(entries, tarEntry{name: "argent", data: []byte("ok")})
	archive := tarGzFixture(t, entries...)
	server := serveBytes(t, archive)
	stage := filepath.Join(t.TempDir(), "argent.tar.gz")
	err := Download(context.Background(),
		Asset{Name: "argent.tar.gz", URL: server.URL, Size: int64(len(archive))},
		stage, checksumOf(archive))
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("error = %v, want an entry-limit error", err)
	}
}

func TestApplyRefusesToInstallARawGzipArchive(t *testing.T) {
	archive := tarGzFixture(t, tarEntry{name: "argent", data: []byte("MZ")})
	dir := t.TempDir()
	stage := filepath.Join(dir, "argent.tar.gz")
	if err := os.WriteFile(stage, archive, 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	target := filepath.Join(dir, "argent")
	if err := os.WriteFile(target, []byte("old build"), 0o755); err != nil {
		t.Fatalf("WriteFile(target): %v", err)
	}
	if err := Apply(stage, target); err == nil || !strings.Contains(err.Error(), "gzip archive") {
		t.Fatalf("error = %v, want a gzip-archive refusal", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "old build" {
		t.Fatalf("target = %q, err %v; it must be left untouched", got, err)
	}
}
