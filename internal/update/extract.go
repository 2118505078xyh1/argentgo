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
	// maxArchiveEntries 限制我们在下载的 zip 中检查的条目数量。
	// 一个发布压缩包只会携带几个二进制文件。
	maxArchiveEntries = 256
	// maxExtractedEntry 限制单个未压缩条目（512 MiB）。
	maxExtractedEntry = 512 << 20
	// maxExtractedTotal 限制所有解包字节之和（1 GiB），因此
	// 压缩炸弹无法膨胀超过下载上限。
	maxExtractedTotal = 1 << 30
)

// executableNames 是唯一有资格安装的 zip 条目。
// 发布压缩包还附带 argentgo-native.exe，它绝不能替换
// 正在运行的 CLI 二进制文件。
// 旧键 argent/argent.exe 必须保留：历史 Release zip 仍用这些名字，
// 去掉它们会让旧版本无法通过自更新升级到新版本。
var executableNames = map[string]bool{
	"argentgo":     true,
	"argentgo.exe": true,
	"argent":       true,
	"argent.exe":   true,
}

// isZipAsset 报告资源名是否表示 zip 压缩包。
func isZipAsset(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".zip")
}

// isTarGzAsset 报告资源名是否表示 gzip 压缩的 tar
// 压缩包。FindAsset 在非 Windows 平台上给 .tar.gz/.tgz 最高的扩展名加分，
// 因此 Download 必须像解包 zip 一样解包它们，否则会
// 暂存原始压缩包并损坏二进制文件。
func isTarGzAsset(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz")
}

// extractExecutable 从 zip 压缩包中将白名单中的 argentgo 可执行文件解包
// 到 dir，并返回解包文件的路径。
//
// 解包刻意受到限制：绝对路径、".." 遍历、
// 符号链接及其他非普通条目都会被拒绝；条目数和字节
// 总量有上限；且只写入 argentgo/argentgo.exe（旧包为 argent/argent.exe）。
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
			return "", errors.New("zip archive contains more than one argentgo executable")
		}
		chosen, chosenName = entry, base
	}
	if chosen == nil {
		return "", errors.New("zip archive has no argentgo or argentgo.exe entry")
	}

	// chosenName 是来自 safeArchivePath 的普通基础名，因此无法逃逸
	// 解包目录。
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

// extractExecutableFromTarGz 从 gzip 压缩的 tar 压缩包中
// 将白名单中的 argentgo 可执行文件解包到 dir，并返回解包文件的
// 路径。它应用与 extractExecutable 相同的限制：路径和符号链接
// 检查、条目/字节上限，以及单个白名单可执行文件。解压后的
// 流整体受限，因此无论 tar 头声称什么，压缩炸弹都无法膨胀超过
// 单条目上限。
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

	// tar.Reader 是只向前的流，因此在跳过条目时应限制解压总字节数，
	// 而不是信任单个头部的大小。
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
			// 普通文件；在下面的白名单中检查。
		default:
			// 符号链接、硬链接、设备、FIFO 等绝不能安装。
			_ = os.Remove(dest)
			return "", fmt.Errorf("unsupported tar entry %q", header.Name)
		}
		base := path.Base(name)
		if !executableNames[strings.ToLower(base)] {
			continue
		}
		if dest != "" {
			_ = os.Remove(dest)
			return "", errors.New("tar archive contains more than one argentgo executable")
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

		// base 是 safeArchivePath 返回的普通名称，因此无法逃逸
		// 解包目录。
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
		return "", errors.New("tar archive has no argentgo or argentgo.exe entry")
	}
	return dest, nil
}

// safeArchivePath 校验压缩包条目名，规范化 Windows
// 分隔符，并返回清理后的斜杠分隔路径。它拒绝
// 绝对路径、盘符以及任何逃逸其父目录的路径。
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
