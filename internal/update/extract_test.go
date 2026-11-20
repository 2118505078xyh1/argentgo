package update

import (
	"archive/zip"
	"bytes"
	"context"
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
