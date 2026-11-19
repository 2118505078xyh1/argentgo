package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checksumOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestDownloadWritesAndVerifiesTheAsset(t *testing.T) {
	payload := []byte("executable bytes")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write(payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "downloads", "argent.exe")
	var calls []int64
	var totals []int64
	err := DownloadProgress(context.Background(),
		Asset{Name: "argent.exe", URL: server.URL, Size: int64(len(payload))},
		dest, checksumOf(payload),
		func(done, total int64) {
			calls = append(calls, done)
			totals = append(totals, total)
		})
	if err != nil {
		t.Fatalf("DownloadProgress: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("dest = %q, want %q", got, payload)
	}
	if len(calls) == 0 {
		t.Fatal("progress was never called")
	}
	for index := 1; index < len(calls); index++ {
		if calls[index] < calls[index-1] {
			t.Fatalf("progress went backwards: %v", calls)
		}
	}
	if calls[len(calls)-1] != int64(len(payload)) {
		t.Fatalf("final progress = %d, want %d", calls[len(calls)-1], len(payload))
	}
	if totals[0] != int64(len(payload)) {
		t.Fatalf("total = %d, want %d", totals[0], len(payload))
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "argent.exe" {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestDownloadAcceptsAPrefixedChecksum(t *testing.T) {
	payload := []byte("payload")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write(payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "asset.bin")
	if err := Download(context.Background(), Asset{Name: "a", URL: server.URL}, dest,
		"sha256:"+strings.ToUpper(checksumOf(payload))); err != nil {
		t.Fatalf("Download: %v", err)
	}
}

func TestDownloadRejectsAChecksumMismatch(t *testing.T) {
	payload := []byte("tampered payload")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Write(payload)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "asset.bin")
	err := Download(context.Background(), Asset{Name: "a", URL: server.URL}, dest,
		checksumOf([]byte("something else")))
	if err == nil {
		t.Fatal("Download accepted a mismatched checksum")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("dest exists after a mismatch: %v", statErr)
	}
}

func TestDownloadRequiresAChecksum(t *testing.T) {
	cases := []string{"", "   ", "abc", "sha256:zzzz"}
	for _, checksum := range cases {
		err := Download(context.Background(), Asset{Name: "a", URL: "https://example.invalid/x"}, "dest", checksum)
		if err == nil {
			t.Fatalf("Download accepted checksum %q", checksum)
		}
	}
}

func TestDownloadRejectsLargeAssets(t *testing.T) {
	err := Download(context.Background(),
		Asset{Name: "huge", URL: "https://example.invalid/x", Size: MaxAssetSize + 1},
		"dest", strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error = %v, want a size limit error", err)
	}
}

func TestDownloadReportsHTTPFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	dest := filepath.Join(t.TempDir(), "asset.bin")
	err := Download(context.Background(), Asset{Name: "a", URL: server.URL}, dest, strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want an HTTP 404 error", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("dest exists after a failed download")
	}
}

func TestDownloadStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dest := filepath.Join(t.TempDir(), "asset.bin")
	err := Download(ctx, Asset{Name: "a", URL: "https://example.invalid/x"}, dest, strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("Download succeeded with a cancelled context")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("dest exists after cancellation")
	}
}
