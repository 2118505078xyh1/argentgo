package plugins

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrUnsafeArchive 报告会离开安装目录的归档条目
// ErrUnsafeArchive 报告会离开安装目录的归档条目（"zip slip"）。InstallOptions 调整安装。Force 替换同名的现有安装。
var ErrUnsafeArchive = errors.New("plugins: archive entry escapes the target directory")

const (
	maxZipEntries = 10000
	maxZipBytes   = 256 << 20
	stagingPrefix = ".install-"
)

// InstallOptions 调整安装。
type InstallOptions struct {
	// Force 替换同名的现有安装。
	Force bool
}

// Install 把本地目录或 .zip 安装到 pluginsDir。已存在的安装是错误；使用带 Force 的 InstallWith 来替换它。
// 已存在的安装是错误；使用带 Force 的 InstallWith 来替换它。
func Install(source, pluginsDir string) (*Bundle, error) {
	return InstallWith(source, pluginsDir, InstallOptions{})
}

// InstallWith 安装一个 bundle 并启用它。目录源必须包含清单。.zip 源先被解压到暂存目录；每个条目都对照 pluginsDir 检查，因此名为 "../x" 的归档成员被拒绝而非写到外面。
//
// 目录源必须包含清单。.zip 源先被解压到
// 暂存目录；每个条目都对照 pluginsDir 检查，因此
// 名为 "../x" 的归档成员被拒绝而非写到外面。
func InstallWith(source, pluginsDir string, opts InstallOptions) (*Bundle, error) {
	source = filepath.Clean(source)
	info, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("plugins: read source %s: %w", source, err)
	}
	if pluginsDir == "" {
		return nil, errors.New("plugins: install needs a plugins directory")
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, fmt.Errorf("plugins: create %s: %w", pluginsDir, err)
	}

	bundleRoot := source
	if !info.IsDir() {
		staging, err := os.MkdirTemp(pluginsDir, stagingPrefix)
		if err != nil {
			return nil, fmt.Errorf("plugins: create staging directory: %w", err)
		}
		defer os.RemoveAll(staging)
		if err := extractZipFile(source, staging); err != nil {
			return nil, err
		}
		bundleRoot, err = locateBundleRoot(staging)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", source, err)
		}
	}

	manifest, err := LoadManifest(bundleRoot)
	if err != nil {
		return nil, err
	}
	destination := filepath.Join(pluginsDir, manifest.Name)
	if filepath.Clean(bundleRoot) == destination {
		return nil, fmt.Errorf("plugins: %q is already installed at %s", manifest.Name, destination)
	}

	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(destination)
			if err != nil || !within(pluginsDir, resolved) {
				return nil, fmt.Errorf("plugins: install target %s is a symlink leaving %s", destination, pluginsDir)
			}
		}
		if !opts.Force {
			return nil, fmt.Errorf("plugins: %q is already installed at %s; set Force to replace it", manifest.Name, destination)
		}
		if err := os.RemoveAll(destination); err != nil {
			return nil, fmt.Errorf("plugins: remove %s: %w", destination, err)
		}
	}

	if err := copyTree(bundleRoot, destination); err != nil {
		return nil, err
	}
	installed, err := LoadManifest(destination)
	if err != nil {
		return nil, err
	}
	if err := SetEnabled(pluginsDir, installed.Name, true); err != nil {
		return nil, err
	}
	return &Bundle{
		Name:     installed.Name,
		Version:  installed.Version,
		Dir:      installed.Root,
		Manifest: installed,
		Enabled:  true,
		Problems: installed.Validate(),
	}, nil
}

// Uninstall 按名称移除已安装的 bundle，但仅当该目录的清单与名称一致，使拼写错误不会删除无关文件夹。locateBundleRoot 在解压的归档中查找持有清单的目录：根本身或单个顶层文件夹。
// 的清单与名称一致，使拼写错误不会删除无关文件夹。
func Uninstall(name, pluginsDir string) (bool, error) {
	if err := ValidateName(name); err != nil {
		return false, err
	}
	candidate := filepath.Join(pluginsDir, name)
	manifest, err := LoadManifest(candidate)
	if err != nil || manifest.Name != name {
		return false, nil
	}
	if err := os.RemoveAll(candidate); err != nil {
		return false, fmt.Errorf("plugins: remove %s: %w", candidate, err)
	}
	if err := SetEnabled(pluginsDir, name, false); err != nil {
		return true, err
	}
	return true, nil
}

// locateBundleRoot 在解压的归档中查找
// 持有清单的目录：根本身或单个顶层文件夹。
func locateBundleRoot(dir string) (string, error) {
	if _, ok := FindManifest(dir); ok {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("plugins: read %s: %w", dir, err)
	}
	var candidates []string
	for _, entry := range entries {
		candidate := filepath.Join(dir, entry.Name())
		if !isDir(candidate) {
			continue
		}
		if _, ok := FindManifest(candidate); ok {
			candidates = append(candidates, candidate)
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", ErrNoManifest
	default:
		return "", fmt.Errorf("plugins: archive contains %d plugin directories", len(candidates))
	}
}

func extractZipFile(archivePath, destination string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("plugins: open archive %s: %w", archivePath, err)
	}
	defer reader.Close()
	if len(reader.File) > maxZipEntries {
		return fmt.Errorf("plugins: archive %s has %d entries, more than %d", archivePath, len(reader.File), maxZipEntries)
	}

	var written int64
	for _, entry := range reader.File {
		target, err := safeArchivePath(destination, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("plugins: create %s: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("plugins: create %s: %w", filepath.Dir(target), err)
		}
		if err := copyZipEntry(entry, target, &written); err != nil {
			return err
		}
	}
	return nil
}

func copyZipEntry(entry *zip.File, target string, written *int64) error {
	in, err := entry.Open()
	if err != nil {
		return fmt.Errorf("plugins: open archive entry %q: %w", entry.Name, err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("plugins: create %s: %w", target, err)
	}
	remaining := maxZipBytes - *written
	// 多出的一个字节证明归档过大，而非静默
	// 截断文件。
	count, copyErr := io.Copy(out, io.LimitReader(in, remaining+1))
	closeErr := out.Close()
	if copyErr != nil {
		return fmt.Errorf("plugins: extract %q: %w", entry.Name, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("plugins: close %s: %w", target, closeErr)
	}
	*written += count
	if *written > maxZipBytes {
		return fmt.Errorf("plugins: archive expands beyond %d bytes", int64(maxZipBytes))
	}
	return nil
}

// safeArchivePath 把一个归档成员映射到目标，拒绝
// 绝对路径、盘符、".." 组件及任何解析到外部的路径。
func safeArchivePath(destination, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("%w: empty entry name", ErrUnsafeArchive)
	}
	if strings.ContainsRune(name, 0) || strings.ContainsRune(name, ':') {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, name)
	}
	cleaned := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if cleaned == "." {
		return destination, nil
	}
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, name)
	}
	target := filepath.Join(destination, filepath.FromSlash(cleaned))
	if !within(destination, target) || filepath.Clean(target) == filepath.Clean(destination) {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArchive, name)
	}
	return target, nil
}

// safeArchivePath 把一个归档成员映射到目标，拒绝绝对路径、盘符、".." 组件及任何解析到外部的路径。copyTree 复制目录，跳过 .git 使检出能干净安装。
func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("plugins: create %s: %w", destination, err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("plugins: read %s: %w", source, err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		info, err := os.Lstat(from)
		if err != nil {
			return fmt.Errorf("plugins: read %s: %w", from, err)
		}
		// 绝不跟随符号链接：指向祖先（或自身）的链接会
		// 在 os.Stat 下无限递归，指向外部目录的链接
		// 绝不跟随符号链接：指向祖先（或自身）的链接会在 os.Stat 下无限递归，指向外部目录的链接会复制 bundle 之外的文件。Lstat 报告链接
		// 本身，所以跳过它而非当作目录/文件。
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		switch {
		case info.IsDir():
			if err := copyTree(from, to); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := copyFile(from, to); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("plugins: read %s: %w", source, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("plugins: create %s: %w", filepath.Dir(destination), err)
	}
	out, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("plugins: create %s: %w", destination, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("plugins: copy %s to %s: %w", source, destination, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("plugins: close %s: %w", destination, err)
	}
	return nil
}
