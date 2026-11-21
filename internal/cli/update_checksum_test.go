package cli

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

	"github.com/2118505078xyh1/argentgo/internal/update"
)

func checksumForTest(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// checksumServer 提供一个把所有请求都指向同一份清单的发布布局。
func checksumServer(t *testing.T, manifest string) (*update.Release, *http.Client) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(manifest))
	}))
	t.Cleanup(server.Close)
	release := update.Release{Assets: []update.Asset{
		{Name: "checksums.txt", URL: server.URL},
	}}
	return &release, server.Client()
}

// 多资产清单必须按资产名取哈希；旧实现取第一个 64 位十六进制串，
// 会为第二个资产返回第一个资产的哈希，导致校验失败或错误放行。
func TestFetchReleaseChecksumMatchesByName(t *testing.T) {
	hashWindows := checksumForTest("windows binary")
	hashDarwin := checksumForTest("darwin binary")
	manifest := hashWindows + "  argent-windows-amd64.exe\n" +
		hashDarwin + "  argent-darwin-arm64.tar.gz\n"
	release, client := checksumServer(t, manifest)
	release.Assets = append(release.Assets,
		update.Asset{Name: "argent-windows-amd64.exe"},
		update.Asset{Name: "argent-darwin-arm64.tar.gz"},
	)

	for _, tc := range []struct {
		name string
		want string
	}{
		{"argent-windows-amd64.exe", hashWindows},
		{"argent-darwin-arm64.tar.gz", hashDarwin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fetchReleaseChecksum(context.Background(), *release,
				update.Asset{Name: tc.name}, client)
			if err != nil {
				t.Fatalf("fetchReleaseChecksum: %v", err)
			}
			if got != tc.want {
				t.Fatalf("checksum = %q, want %q", got, tc.want)
			}
		})
	}
}

// 文件名带 "./" 前缀、二进制模式（" *"）以及大小写差异都应正确匹配。
func TestFetchReleaseChecksumAcceptsManifestVariants(t *testing.T) {
	want := checksumForTest("binary")
	// 前导空白与 "./" 前缀都应在解析时被规范化，名称比较不区分大小写。
	manifest := "  " + want + "  ./argent-tool.exe\n"
	release, client := checksumServer(t, manifest)
	release.Assets = append(release.Assets, update.Asset{Name: "argent-tool.exe"})

	got, err := fetchReleaseChecksum(context.Background(), *release,
		update.Asset{Name: "ARGENT-TOOL.EXE"}, client)
	if err != nil {
		t.Fatalf("fetchReleaseChecksum: %v", err)
	}
	if got != want {
		t.Fatalf("checksum = %q, want %q", got, want)
	}
}

func TestFetchReleaseChecksumFailsClosed(t *testing.T) {
	hashOne := checksumForTest("one")
	hashTwo := checksumForTest("two")

	t.Run("conflict", func(t *testing.T) {
		manifest := hashOne + "  app.exe\n" + hashTwo + "  app.exe\n"
		release, client := checksumServer(t, manifest)
		release.Assets = append(release.Assets, update.Asset{Name: "app.exe"})
		_, err := fetchReleaseChecksum(context.Background(), *release,
			update.Asset{Name: "app.exe"}, client)
		if err == nil || !strings.Contains(err.Error(), "conflicting") {
			t.Fatalf("err = %v, want a conflict complaint", err)
		}
	})

	t.Run("missing target", func(t *testing.T) {
		manifest := hashOne + "  app.exe\n"
		release, client := checksumServer(t, manifest)
		release.Assets = append(release.Assets,
			update.Asset{Name: "app.exe"},
			update.Asset{Name: "other.exe"},
		)
		_, err := fetchReleaseChecksum(context.Background(), *release,
			update.Asset{Name: "other.exe"}, client)
		if err == nil || !strings.Contains(err.Error(), "no SHA-256 checksum") {
			t.Fatalf("err = %v, want a missing checksum complaint", err)
		}
	})

	t.Run("orphan", func(t *testing.T) {
		manifest := hashOne + "  app.exe\n" + hashTwo + "  ghost.exe\n"
		release, client := checksumServer(t, manifest)
		release.Assets = append(release.Assets, update.Asset{Name: "app.exe"})
		_, err := fetchReleaseChecksum(context.Background(), *release,
			update.Asset{Name: "app.exe"}, client)
		if err == nil || !strings.Contains(err.Error(), "orphan") {
			t.Fatalf("err = %v, want an orphan complaint", err)
		}
	})
}

// --sha256 手动覆盖必须仍然跳过自动清单查询（包括按名匹配的整条路径）。
func TestUpdateApplyWithManualSHA256SkipsFetch(t *testing.T) {
	storage := t.TempDir()
	target := filepath.Join(storage, "installed.exe")
	binary := []byte("new binary")
	checksum := checksumForTest(string(binary))
	ops := &UpdateOps{
		Check: func(context.Context, string, string, *http.Client) (update.Release, bool, error) {
			return update.Release{Tag: "v2.0.0", Assets: []update.Asset{{Name: "app.exe", URL: "u"}}}, true, nil
		},
		Checksum: func(context.Context, update.Release, update.Asset, *http.Client) (string, error) {
			t.Fatal("checksum fetch must be skipped when --sha256 is supplied")
			return "", nil
		},
		Download: func(_ context.Context, _ update.Asset, dest, want string) error {
			if want != checksum {
				t.Fatalf("download checksum = %q, want %q", want, checksum)
			}
			return os.WriteFile(dest, binary, 0o644)
		},
		Apply: func(string, string) error { return nil },
	}
	ioState, _, errOut := newTestIO("", nil)
	code := RunWith(App{IO: ioState, StorageDir: storage, UpdateOps: ops}, []string{
		"update", "--apply", "--asset", "app.exe", "--sha256", checksum, "--target", target,
	})
	if code != 0 {
		t.Fatalf("exit = %d, err %q", code, errOut.String())
	}
}

// 单哈希清单（裸摘要，无文件名）必须与旧实现逐字节一致：仍返回该摘要。
func TestFetchReleaseChecksumSingleHashCompatibility(t *testing.T) {
	want := checksumForTest("only binary")
	for _, manifest := range []string{
		want + "\n",
		"sha256:" + want + "\n",
		want + "  app.exe\n",
	} {
		release, client := checksumServer(t, manifest)
		release.Assets = append(release.Assets, update.Asset{Name: "app.exe"})
		got, err := fetchReleaseChecksum(context.Background(), *release,
			update.Asset{Name: "app.exe"}, client)
		if err != nil {
			t.Fatalf("manifest %q: fetchReleaseChecksum: %v", manifest, err)
		}
		if got != want || len(got) != sha256.Size*2 {
			t.Fatalf("manifest %q: checksum = %q, want %q", manifest, got, want)
		}
	}
}
