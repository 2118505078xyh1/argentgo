package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// candidate 是一个发现的指令文件及其排序输入。
// distance 是该文件与发现它的活动目录之间的目录层级数；
// seq 在平局时保留发现顺序。
type candidate struct {
	path     string
	distance int
	seq      int
}

// LoadForDirs 为一组活动子目录及其祖先收集指令文件，
// 停在工作区根目录。它用于
// "嵌套 AGENTS.md" 的发现：当工作涉及 src/parser 和 src/lexer 时，
// 调用方传入这些目录，使每个子树的特定规则
// （以及它们之上的共享规则）包含在系统提示词中。
//
// 输入的 dirs 相对于 cwd 解析。对每个目录，考虑直到工作区根
// （含）的每一级祖先，按最近的优先：更深
// 目录中的文件优先于靠近根的同名文件。
// 每个目录中都会读取两个 FileNames。路径按其
// 解析后的位置（绝对、清理、Windows 上大小写折叠）去重，使
// 多个子树共享的文件只注入一次，且位于其最具体的排名处。
//
// 现有 Load 行为不变；LoadForDirs 是附加 API。
// 此处适用与 LoadIn 相同的单文件上限和总量字节预算，且
// 返回的 docs 已裁剪并排好序，可直接用于 Block()。无法
// 解析或读取的目录会被跳过，而不是导致整个调用失败。
func LoadForDirs(cwd string, dirs []string) []Doc {
	workspace, err := filepath.Abs(cwd)
	if err != nil {
		return nil
	}
	workspace = filepath.Clean(workspace)
	if info, statErr := os.Stat(workspace); statErr != nil || !info.IsDir() {
		return nil
	}

	// 每条不同祖先链的排名列表。到活动目录的
	// 距离越短越具体；平局时保持首次出现的顺序。
	seen := make(map[string]candidate)
	seq := 0
	normalized := make(map[string]bool)

	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		abs := dir
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(workspace, abs)
		}
		abs, err = filepath.Abs(abs)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		if !withinWorkspace(abs, workspace) {
			// 绝不遍历到工作区根目录之外。
			continue
		}
		key := normalizePath(abs)
		if normalized[key] {
			continue
		}
		normalized[key] = true

		distance := 0
		current := abs
		for {
			seq++
			for _, name := range FileNames {
				path := filepath.Join(current, name)
				pKey := normalizePath(path)
				if old, exists := seen[pKey]; !exists || distance < old.distance {
					seen[pKey] = candidate{path: path, distance: distance, seq: seq}
				}
			}
			if samePath(current, workspace) {
				break
			}
			parent := filepath.Dir(current)
			if parent == current || !withinWorkspace(parent, workspace) {
				break
			}
			current = parent
			distance++
		}
	}

	ranked := make([]candidate, 0, len(seen))
	for _, c := range seen {
		ranked = append(ranked, c)
	}
	sortCandidates(ranked)

	var docs []Doc
	used := 0
	for _, c := range ranked {
		info, err := os.Stat(c.path)
		if err != nil || info.IsDir() {
			continue
		}
		body, fileTruncated, err := readInstructionFile(c.path, DefaultLimit)
		if err != nil || body == "" {
			continue
		}

		// 当单文件上限已裁剪该正文时，预留截断标记的字节；
		// 参见 LoadIn 中的相应逻辑。
		markerCost := 0
		if fileTruncated {
			markerCost = len(TruncationMarker)
		}
		remaining := totalBudget - used
		if remaining <= markerCost {
			break
		}
		budgetTruncated := false
		if len(body)+markerCost > remaining {
			room := remaining - len(TruncationMarker)
			if room <= 0 {
				break
			}
			body = clipRunes(body, room)
			budgetTruncated = true
		}

		doc := Doc{Path: c.path, Text: body, Truncated: fileTruncated || budgetTruncated}
		if doc.Truncated {
			doc.Text = strings.TrimRight(doc.Text, " \t\r\n") + TruncationMarker
		}
		docs = append(docs, doc)
		used += len(doc.Text)
		if budgetTruncated || used >= totalBudget {
			break
		}
	}
	return docs
}

// withinWorkspace 报告 path 是否为工作区根目录或其下的路径。
// 比较在清理后的绝对路径上进行，并在 Windows 上大小写
// 不敏感，与 LoadIn 使用的 samePath 一致。
func withinWorkspace(path, workspace string) bool {
	if samePath(path, workspace) {
		return true
	}
	prefix := workspace + string(filepath.Separator)
	if filepath.Separator == '\\' {
		return strings.HasPrefix(strings.ToLower(path), strings.ToLower(prefix))
	}
	return strings.HasPrefix(path, prefix)
}

// normalizePath 返回用于去重的规范键。Windows 路径
// 不区分大小写，因此折叠；其他平台保留原样大小写。
func normalizePath(path string) string {
	path = filepath.Clean(path)
	if filepath.Separator == '\\' {
		return strings.ToLower(path)
	}
	return path
}

// samePath 比较两个清理后的路径，在 Windows 上不区分大小写。
func samePath(a, b string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// sortCandidates 将 docs 按最具体优先排序：距离更小（更接近
// 活动目录）者优先，然后以较早的发现顺序打破平局，最后
// 按路径文本保证结果集稳定。
func sortCandidates(items []candidate) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			a, b := items[j], items[j-1]
			less := a.distance < b.distance ||
				(a.distance == b.distance && a.seq < b.seq) ||
				(a.distance == b.distance && a.seq == b.seq && a.path < b.path)
			if !less {
				break
			}
			items[j-1], items[j] = a, b
		}
	}
}
