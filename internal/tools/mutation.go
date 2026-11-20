package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MutationSnapshot 是检查点清单中某路径在回合前的状态。
// 缺失的文件以 Exists=false 和空摘要记录，从而
// 检查点可以区分“在本回合中被删除”与“从未存在过”。
type MutationSnapshot struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Exists bool   `json:"exists"`
}

// MutationPaths 是检查点的接缝：它报告一次变更性工具调用
// 可能改变的工作区相对路径。它是注册表上的可选接口，
// 以便 agent 循环保持与 tools 包无关。
func (r *Registry) MutationPaths(name, args string) []string {
	tool, ok := r.tools[name]
	if !ok || !tool.Mutating() {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(args), &fields); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var paths []string
	collect := func(value any) {
		path, ok := value.(string)
		if !ok || strings.TrimSpace(path) == "" {
			return
		}
		absolute, err := resolve(r.root, path)
		if err != nil {
			// 被拒绝的越界路径绝不会成为检查点路径。策略门控
			// 会在工具运行前单独报告它。
			return
		}
		rel, err := filepath.Rel(r.root, absolute)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
		clean := filepath.ToSlash(rel)
		if !seen[clean] {
			seen[clean] = true
			paths = append(paths, clean)
		}
	}
	for _, key := range []string{"path", "file", "dir", "directory", "target"} {
		collect(fields[key])
	}
	if list, ok := fields["paths"].([]any); ok {
		for _, item := range list {
			collect(item)
		}
	}
	sort.Strings(paths)
	return paths
}

// SnapshotMutations 读取检查点清单中每个路径在回合前的字节。
// 词法上越出 root 的路径会被拒绝，而非被悄悄
// 记录。
func SnapshotMutations(root string, paths []string) ([]MutationSnapshot, error) {
	snapshots := make([]MutationSnapshot, 0, len(paths))
	for _, path := range paths {
		absolute, err := resolve(root, path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, absolute)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(absolute)
		if err != nil {
			if os.IsNotExist(err) {
				snapshots = append(snapshots, MutationSnapshot{Path: filepath.ToSlash(rel), Exists: false})
				continue
			}
			return nil, err
		}
		digest := sha256.Sum256(data)
		snapshots = append(snapshots, MutationSnapshot{
			Path:   filepath.ToSlash(rel),
			SHA256: hex.EncodeToString(digest[:]),
			Exists: true,
		})
	}
	return snapshots, nil
}
