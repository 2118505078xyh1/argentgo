package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestWithDownloadDeadline 在没有任何 I/O 的情况下检查截止时间策略：
// 没有截止时间的 context 会获得更新器默认值，而调用方提供的
// 截止时间绝不会被替换。
func TestWithDownloadDeadline(t *testing.T) {
	original := downloadTimeout
	t.Cleanup(func() { downloadTimeout = original })
	downloadTimeout = 2 * time.Hour

	cases := []struct {
		name         string
		ctx          func() (context.Context, context.CancelFunc)
		wantDeadline time.Duration
	}{
		{
			name: "no caller deadline gets the download default",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.Background(), func() {}
			},
			wantDeadline: downloadTimeout,
		},
		{
			name: "a shorter caller deadline is preserved",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			},
			wantDeadline: 50 * time.Millisecond,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			ctx, outerCancel := test.ctx()
			defer outerCancel()
			got, cancel := withDownloadDeadline(ctx)
			defer cancel()

			deadline, ok := got.Deadline()
			if !ok {
				t.Fatal("download context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining < test.wantDeadline-time.Second || remaining > test.wantDeadline+time.Second {
				t.Fatalf("deadline in %v, want about %v", remaining, test.wantDeadline)
			}
		})
	}
}

// TestDownloadAppliesANetworkTimeout 是端到端回归测试：一个
// 接受连接后停滞的服务器绝不能让更新器挂起，而调用方
// 的截止时间仍必须优先。全部使用本地模拟 HTTP 服务器。
func TestDownloadAppliesANetworkTimeout(t *testing.T) {
	original := downloadTimeout
	t.Cleanup(func() { downloadTimeout = original })

	payload := []byte("payload")
	stalled := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(stalled.Close)
	prompt := serveBytes(t, payload)

	cases := []struct {
		name        string
		url         string
		timeout     time.Duration
		ctx         func() (context.Context, context.CancelFunc)
		wantErr     bool
		wantTimeout bool
	}{
		{
			name:        "a stalled server hits the default download timeout",
			url:         stalled.URL,
			timeout:     40 * time.Millisecond,
			wantErr:     true,
			wantTimeout: true,
		},
		{
			name:    "a shorter caller deadline is not overridden",
			url:     stalled.URL,
			timeout: 10 * time.Second,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 40*time.Millisecond)
			},
			wantErr:     true,
			wantTimeout: true,
		},
		{
			name:    "a prompt response still succeeds",
			url:     prompt.URL,
			timeout: 10 * time.Second,
			wantErr: false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			downloadTimeout = test.timeout
			ctx := context.Background()
			if test.ctx != nil {
				var cancel context.CancelFunc
				ctx, cancel = test.ctx()
				defer cancel()
			}
			start := time.Now()
			dest := filepath.Join(t.TempDir(), "asset.bin")
			err := Download(ctx, Asset{Name: "a", URL: test.url, Size: int64(len(payload))},
				dest, checksumOf(payload))
			elapsed := time.Since(start)

			if (err != nil) != test.wantErr {
				t.Fatalf("Download error = %v, wantErr %v", err, test.wantErr)
			}
			if test.wantTimeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want context deadline exceeded", err)
			}
			if test.wantErr {
				if elapsed > 2*time.Second {
					t.Fatalf("download took %v; the network deadline was not applied", elapsed)
				}
				if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
					t.Fatalf("dest exists after a timeout: %v", statErr)
				}
				return
			}
			got, readErr := os.ReadFile(dest)
			if readErr != nil || string(got) != string(payload) {
				t.Fatalf("dest = %q, err %v", got, readErr)
			}
		})
	}
}
