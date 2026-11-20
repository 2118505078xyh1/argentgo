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
	"time"
)

// MaxAssetSize 限制一次发布下载（512 MiB），使恶意或异常的
// 响应无法填满磁盘。
const MaxAssetSize int64 = 512 << 20

// downloadTimeout 在调用方 context 没有自身截止时间时，限制一次自更新下载的
// 时长。Check 使用 30 秒的客户端超时；下载需要
// 同样的保护，否则一个接受连接却从不发送数据（或中途停止）的服务器
// 会让更新器永远挂起。它是变量，以便测试可以缩短它而无需等待；
// 生产环境始终使用 30 秒。
var downloadTimeout = 30 * time.Second

// withDownloadDeadline 在 ctx 尚无截止时间时为其设置一个。
// 已存在的、可能更短的调用方截止时间始终被保留。
func withDownloadDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, downloadTimeout)
}

// Progress 在每个分块之后被调用，传入到目前为止已写入的字节数和
// 预期总量（服务器未报告时为 0）。
type Progress func(done, total int64)

// Download 将 asset 获取到 dest，并在文件出现在 dest 之前验证其 SHA-256 校验和。
// 校验和不匹配会删除部分下载的文件。对于 zip 或 gzip 压缩的 tar 资源，
// 已验证的压缩包以受限方式解包，白名单中的可执行文件（argent/argent.exe）
// 被放到 dest；而裸二进制则
// 原样写入。
func Download(ctx context.Context, asset Asset, dest, checksum string) error {
	return DownloadProgress(ctx, asset, dest, checksum, nil)
}

// DownloadProgress 是带进度回调的 Download。
func DownloadProgress(ctx context.Context, asset Asset, dest, checksum string, progress Progress) error {
	ctx, cancel := withDownloadDeadline(ctx)
	defer cancel()

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
	if isZipAsset(asset.Name) || isTarGzAsset(asset.Name) {
		// 已发布的校验和覆盖的是压缩包，因此上面已经验证过。
		// 将可执行文件解包到私有暂存目录，
		// 再把它放到 dest；直接安装压缩包会损坏
		// 二进制文件。FindAsset 在非 Windows 平台上优先选择 .tar.gz/.tgz，因此
		// 这里也必须解包它们（而不是把原始 gzip 文件暂存）。
		extractDir, err := os.MkdirTemp(directory, ".extract-*")
		if err != nil {
			return fmt.Errorf("update: create an extraction directory in %s: %w", directory, err)
		}
		defer os.RemoveAll(extractDir)
		var binary string
		if isTarGzAsset(asset.Name) {
			binary, err = extractExecutableFromTarGz(tempName, extractDir)
		} else {
			binary, err = extractExecutable(tempName, extractDir)
		}
		if err != nil {
			return fmt.Errorf("update: extract %s: %w", asset.Name, err)
		}
		if err := os.Rename(binary, dest); err != nil {
			return fmt.Errorf("update: place %s: %w", dest, err)
		}
		return nil
	}
	if err := os.Chmod(tempName, 0o755); err != nil {
		return fmt.Errorf("update: mark %s executable: %w", tempName, err)
	}
	if err := os.Rename(tempName, dest); err != nil {
		return fmt.Errorf("update: place %s: %w", dest, err)
	}
	return nil
}

// normaliseChecksum 接受裸十六进制摘要或 "sha256:<hex>"。
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
