package uri

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ResolveWithFallback 解析 argent:// URI；若 scheme 未注册（如 native 二进制
// 未注入 docs handler），可用 fallbackRoot 直接从文件系统读取 docs/<path>。
// 这让同一份 argent:// 引用在"有注册表"和"无注册表"两种环境下都能工作。
//
// fallbackRoot 为空时退化为普通 Resolve（无文件系统回退）。
func ResolveWithFallback(raw, fallbackRoot string) (Resource, error) {
	res, err := ResolveDefault(raw)
	if err == nil {
		return res, nil
	}
	if !errors.Is(err, ErrUnknownScheme) || fallbackRoot == "" {
		return Resource{}, err
	}
	scheme, path, perr := Parse(raw)
	if perr != nil || scheme != "docs" {
		return Resource{}, err // 只对 docs scheme 做文件系统回退
	}
	// 防路径逃逸：Parse 已拒绝 .. 段；这里再确保解析后的路径仍在 fallbackRoot 内。
	name := strings.TrimPrefix(path, "/")
	if name == "" {
		name = "README.md"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		name += ".md"
	}
	full := filepath.Join(fallbackRoot, filepath.FromSlash(name))
	rel, err := filepath.Rel(fallbackRoot, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Resource{}, fmt.Errorf("uri: path escape in %q", raw)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Resource{}, fmt.Errorf("%w: docs/%s", ErrNotFound, name)
		}
		return Resource{}, err
	}
	return Resource{Scheme: "docs", Path: name, Content: string(data)}, nil
}
