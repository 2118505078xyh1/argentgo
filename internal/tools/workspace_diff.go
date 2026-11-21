package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// 工作树差异发现原语。它不依赖 git，只用标准库扫描文件系统，供检查点/撤销
// 流程 opt-in 调用（当前刻意没有调用方，避免改变现有工具的默认行为）。
// 它只观察文件系统，不修改工作树，也不修改调用方传入的快照。

const (
	// workspaceDiffMaxChanges 限制单次报告的路径数量，避免海量间接改动撑爆上下文。
	workspaceDiffMaxChanges = 1000
	// workspaceDiffFileReadLimit 是单个文件参与哈希的最大字节数。
	// 只读取前缀使二进制/超大文件的扫描开销保持有界。
	workspaceDiffFileReadLimit = 1 << 20
	// workspaceDiffHashBytes 是保留的 SHA-256 摘要前若干字节数，
	// 让快照中的摘要字符串保持紧凑，同时仍足以区分内容变化。
	workspaceDiffHashBytes = 16
)

// workspaceDiffIgnoredDirs 是扫描时整棵跳过的噪目录名。名字集合与
// repo_map 的拒绝列表对齐，但独立定义，使一个工具的语义调整不会
// 意外改变差异发现的结果。
var workspaceDiffIgnoredDirs = map[string]bool{
	".git": true, "node_modules": true, "target": true, "dist": true,
	".argent": true, "vendor": true, ".idea": true, ".vscode": true,
	"build": true, "out": true, "__pycache__": true, ".venv": true,
	"venv": true, ".cache": true, ".next": true, "coverage": true,
}

// workspaceDiffHashFile 计算单个普通文件的摘要：最多读取前
// workspaceDiffFileReadLimit 个字节，取 SHA-256 摘要的前
// workspaceDiffHashBytes 字节转成十六进制。非普通文件（符号链接、
// 设备、目录等）返回 false，不会被记录，因此不会跟随链接越界读取。
func workspaceDiffHashFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, workspaceDiffFileReadLimit)); err != nil {
		return "", false
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:workspaceDiffHashBytes]), true
}

// walkWorkspaceFiles 遍历 root 下所有参与对比的普通文件，以 "/"-分隔的
// 相对路径和绝对路径回调 visit。
//
// filepath.WalkDir 本身不跟随符号链接，再配合 entry.Info()（对链接返回
// 链接自身的信息而非目标），符号链接——包括指向 root 外部的链接——会被
// 彻底忽略。噪目录整棵跳过。单个条目的读取错误被忽略，避免一处损坏
// 让整次扫描失败。
func walkWorkspaceFiles(root string, visit func(rel, abs string)) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && workspaceDiffIgnoredDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return nil
		}
		visit(filepath.ToSlash(rel), path)
		return nil
	})
}

// SnapshotWorkspace 扫描 root 并返回“工作区相对路径 -> 摘要”的批次前快照。
// 它跳过噪目录与非普通文件，单文件读取有上限，CJK 路径按 UTF-8 原样保留。
// 只有 root 本身无法访问（不存在或不是目录）时才返回错误；单个文件不可读
// 会被忽略，使快照退化为少一条记录，而不是让整个批次失败。
func SnapshotWorkspace(root string) (map[string]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace_diff: %s is not a directory", root)
	}
	snapshot := make(map[string]string)
	if err := walkWorkspaceFiles(root, func(rel, abs string) {
		if hash, ok := workspaceDiffHashFile(abs); ok {
			snapshot[rel] = hash
		}
	}); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// DiscoverWorkspaceChanges 对比 before 快照与 root 当前内容，返回发生变化
// 的工作区相对路径：新增、内容变化、删除都计入。
//
// 返回列表按字典序稳定排序，最多 workspaceDiffMaxChanges 项；before 绝不
// 被修改。CJK 路径按 UTF-8 字节顺序排序，不会在字符中间被截断。扫描失败
// 按“当前没有可观察文件”处理，因此失效的 root 只会把 before 中的路径
// 报告为删除，而不会 panic 或泄漏越界路径。
func DiscoverWorkspaceChanges(root string, before map[string]string) []string {
	after := make(map[string]string)
	// 扫描错误对差异发现非致命：after 保持为空，before 中的路径记为删除。
	_ = walkWorkspaceFiles(root, func(rel, abs string) {
		if hash, ok := workspaceDiffHashFile(abs); ok {
			after[rel] = hash
		}
	})

	changed := make(map[string]struct{})
	for rel, hash := range after {
		if old, ok := before[rel]; !ok || old != hash {
			changed[rel] = struct{}{}
		}
	}
	for rel := range before {
		if _, ok := after[rel]; !ok {
			changed[rel] = struct{}{}
		}
	}

	paths := make([]string, 0, len(changed))
	for rel := range changed {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	if len(paths) > workspaceDiffMaxChanges {
		paths = paths[:workspaceDiffMaxChanges]
	}
	if len(paths) == 0 {
		return nil
	}
	return paths
}
