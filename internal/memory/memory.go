// Package memory 为系统提示词加载项目指令文件。
//
// 它从工作目录向上查找 AGENTS.md 和 CLAUDE.md，
// 停在用户主目录（当工作目录不在其下时，
// 停在整个文件系统根目录）。文件按最近的优先排序：嵌套的
// 项目文件细化外层文件，渲染器会明确说明这一点。
//
// 每个文件都有上限，因为真实的 CLAUDE.md 可能有数千行，
// 否则每轮都会重发。会报告截断，使模型
// 知道可以自行读取文件全文。文件以增量方式读取（最多
// 多读超过上限一个字节，加上在上限应用之前去掉的前导 BOM），
// 合并后的文本受 totalBudget 限制，因此巨大或
// 深层的目录树不会撑大系统提示词或内存占用。
package memory

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// FileNames 是每个目录中要读取的指令文件，按顺序。
var FileNames = []string{"AGENTS.md", "CLAUDE.md"}

// DefaultLimit 是每个文件保留的字节数。
const DefaultLimit = 8000

// totalBudget 限制从所有文件保留的文本的总大小，因此
// 深层目录树不会在系统提示词中累积数十份受限副本。
const totalBudget = 32 * 1024

// maxDepth 用于终止永远到不了主目录的遍历。
const maxDepth = 32

// TruncationMarker 附加在被缩短的文档末尾。
const TruncationMarker = "\n… [truncated; read the file for the rest]"

// Doc 是向上遍历过程中发现的一个指令文件。
type Doc struct {
	// Path 是文件的绝对路径。
	Path string
	// Text 是文件内容，可能已被截断。
	Text string
	// Truncated 报告 Text 是否因大小限制被截断。
	Truncated bool
}

// Load 从 root 向上遍历，收集 AGENTS.md 和 CLAUDE.md，并停在
// 用户主目录。每个文件以 DefaultLimit 字节为上限。
func Load(root string) ([]Doc, error) {
	home, _ := os.UserHomeDir()
	return LoadIn(root, home, DefaultLimit)
}

// LoadLimit 是带自定义单文件上限的 Load。limit <= 0 表示无上限：在
// 该模式下，单文件上限和全局总量预算都不适用。
func LoadLimit(root string, limit int) ([]Doc, error) {
	home, _ := os.UserHomeDir()
	return LoadIn(root, home, limit)
}

// LoadIn 是带显式主目录的 Load，该目录同时也是遍历
// 边界。测试用它把遍历限制在临时目录树内。
func LoadIn(root, home string, limit int) ([]Doc, error) {
	dir, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory: %s is not a directory", dir)
	}
	stop := ""
	if home != "" {
		if abs, err := filepath.Abs(home); err == nil {
			stop = filepath.Clean(abs)
		}
	}

	var docs []Doc
	used := 0 // combined bytes of the text kept so far
	current := filepath.Clean(dir)
	samePath := func(a, b string) bool {
		if b == "" {
			return false
		}
		if filepath.Separator == '\\' {
			return strings.EqualFold(a, b)
		}
		return a == b
	}

walk:
	for depth := 0; depth < maxDepth; depth++ {
		for _, name := range FileNames {
			path := filepath.Join(current, name)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			body, fileTruncated, err := readInstructionFile(path, limit)
			if err != nil {
				continue
			}
			if body == "" {
				continue
			}

			budgetTruncated := false
			if limit > 0 {
				// 单文件裁剪会在下面附加 TruncationMarker。现在预留
				// 这些字节：否则一个刚好符合剩余空间的受限正文，在
				// 加上标记后会把合并文本推过 totalBudget。按 remaining-marker
				// 重新裁剪可保留该文档，并
				// 使预算精确。
				markerCost := 0
				if fileTruncated {
					markerCost = len(TruncationMarker)
				}
				remaining := totalBudget - used
				if remaining <= markerCost {
					break walk
				}
				if len(body)+markerCost > remaining {
					room := remaining - len(TruncationMarker)
					if room <= 0 {
						break walk
					}
					body = clipRunes(body, room)
					budgetTruncated = true
				}
			}

			doc := Doc{Path: path, Text: body, Truncated: fileTruncated || budgetTruncated}
			if doc.Truncated {
				doc.Text = strings.TrimRight(doc.Text, " \t\r\n") + TruncationMarker
			}
			docs = append(docs, doc)
			used += len(doc.Text)

			if budgetTruncated || used >= totalBudget {
				break walk
			}
		}

		if samePath(current, stop) {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return docs, nil
}

// bom 是 UTF-8 字节顺序标记。出现在文件开头时，它是
// 编码元数据，而非指令内容，因此不应消耗任何
// 单文件字节预算。
const bom = "\ufeff"

// readInstructionFile 读取 path 并返回其裁剪后的文本。当 limit > 0 时
// 它最多读取 limit+1+len(bom) 字节：多出的一个字节仅用于判断
// 文件是否更长；BOM 字节也会被读取，以便在上限应用之前
// 去掉前导 BOM（否则它会悄悄吃掉三个
// 内容字节）。绝不会加载整个文件。返回的文本已
// 按 rune 边界截断。当 limit <= 0 时读取整个文件，且
// truncated 始终为 false（LoadLimit 的"无上限"契约）。
func readInstructionFile(path string, limit int) (text string, truncated bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()

	var raw []byte
	if limit > 0 {
		raw, err = io.ReadAll(io.LimitReader(f, int64(limit)+1+int64(len(bom))))
	} else {
		raw, err = io.ReadAll(f)
	}
	if err != nil {
		return "", false, err
	}

	body := strings.TrimPrefix(string(raw), bom)
	if limit > 0 && len(body) > limit {
		truncated = true
		body = clipRunes(body, limit)
	}
	return strings.TrimSpace(body), truncated, nil
}

// clipRunes 将 s 缩短到最多 limit 字节，且不拆分 UTF-8 rune。
// 调用方保证 len(s) > limit，因此 s[limit] 存在，用于
// 检测被切掉一半的 rune；字节会回退到其起始位置丢弃。
func clipRunes(s string, limit int) string {
	k := limit
	for k > 0 && !utf8.RuneStart(s[k]) {
		k--
	}
	return s[:k]
}

// Block 将 docs 渲染为注入系统提示词的块。
//
// 头部说明了排序规则：条目从最具体
// （最近）的文件到最不具体的文件，因此冲突时以
// 第一个为准。空输入不渲染任何内容。
func Block(docs []Doc) string {
	if len(docs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<project-memory>\n")
	b.WriteString("Instruction files, most specific first: when two files disagree, the earlier (nearer) one wins.\n")
	for _, doc := range docs {
		fmt.Fprintf(&b, "<file path=%q>\n%s\n</file>\n", doc.Path, doc.Text)
	}
	b.WriteString("</project-memory>\n")
	return b.String()
}
