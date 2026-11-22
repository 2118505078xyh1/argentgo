package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ---- read ----

// readWindowCap 限制以某行为中心的窗口的每一侧，使单次调用
// 绝不会倾泻无界数量的上下文。
const readWindowCap = 200

// readMaxBytes 是 read 工具允许整文件读入的尺寸上限。超过上限时拒绝，
// 避免"文件本体 + UTF-8 解码副本 + 全量行切片"三份驻留把峰值内存推到
// 文件大小的数倍。给出替代命令，让模型用搜索/分页工具完成任务。
const readMaxBytes = 10 * 1024 * 1024

// errReadTooLarge 在文件超过 read 工具的尺寸上限时返回，供 errors.Is 匹配。
var errReadTooLarge = errors.New("file too large: use grep_ex to search or bash with sed -n 'START,ENDP' to page")

// readFileLimited 在读入前检查文件大小，超限直接返回 errReadTooLarge。
// maxBytes 由测试注入小值，避免为验证拒绝行为真的创建 10 MB 文件。
// Stat 失败（如文件不存在）时不在这里报错，交给 os.ReadFile 走原有错误路径。
func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	if info, err := os.Stat(path); err == nil && info.Size() > maxBytes {
		return nil, fmt.Errorf("read %s: %w (%d bytes > %d limit)", path, errReadTooLarge, info.Size(), maxBytes)
	}
	return os.ReadFile(path)
}

// utf8BOM 是 UTF-8 字节序标记。它是合法 UTF-8，却会作为 \ufeff
// 留在首行，污染模型上下文与分页行内容，读取时应剥掉。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// stripUTF8BOM 剥掉开头的 UTF-8 BOM（至多一个）。它只返回一个子切片，
// 不修改入参的底层数组；没有 BOM 时原样返回同一切片。
func stripUTF8BOM(data []byte) []byte {
	if len(data) >= len(utf8BOM) && bytes.Equal(data[:len(utf8BOM)], utf8BOM) {
		return data[len(utf8BOM):]
	}
	return data
}

type readTool struct{}

func (readTool) Name() string { return "read" }
func (readTool) Description() string {
	return "Read a UTF-8 text file; prefer this over shell/cat for file contents. " +
		"Long files can be paged with offset (1-based first line) and limit; " +
		"a partial read reports the true total line count. " +
		"For focused context, set line (1-based) with before/after to read a window " +
		"around a target; before/after are capped at 200 each and line wins over offset/limit. " +
		"Set outline=true to read a Go file as a compact symbol list (kind, name, line) " +
		"instead of full source; re-call without outline (optionally with offset/limit) to expand a section. " +
		"Binary files (NUL or dense control bytes) are rejected with a hint to use a suitable tool."
}
func (readTool) Mutating() bool { return false }
func (readTool) Schema() string {
	return `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer","description":"1-based first line"},"limit":{"type":"integer","description":"number of lines"},"line":{"type":"integer","description":"1-based target line; when > 0, wins over offset/limit"},"before":{"type":"integer","description":"lines above target (0-200, capped)"},"after":{"type":"integer","description":"lines below target (0-200, capped)"},"outline":{"type":"boolean","description":"for Go files, return a compact symbol list (kind, name, line) instead of full source; other files fall back to normal read"}},"required":["path"]}`
}

func (t readTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Offset  int    `json:"offset"`
		Limit   int    `json:"limit"`
		Line    int    `json:"line"`
		Before  int    `json:"before"`
		After   int    `json:"after"`
		Outline bool   `json:"outline"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("read: bad arguments: %w", err)
	}
	path, err := resolve(root, args.Path)
	if err != nil {
		return "", err
	}
	data, err := readFileLimited(path, readMaxBytes)
	if err != nil {
		return "", err
	}
	// 先做二进制检测：含 NUL 或高密度控制字节的数据虽然是“合法
	// UTF-8”，但读进模型只会污染上下文，必须带可读提示拒绝。
	if looksLikeBinary(data) {
		return "", fmt.Errorf("read %s: %w", args.Path, errBinaryFile)
	}
	// BOM 是合法 UTF-8，但会作为 \ufeff 留在首行；在编码校验前剥掉，
	// 且不改动入参。GBK 等非 UTF-8 数据没有这个前缀，仍照旧报错。
	data = stripUTF8BOM(data)
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not UTF-8 text", args.Path)
	}
	lines := strings.Split(string(data), "\n")

	// 尾随换行终止最后一行；strings.Split 会在其后留下一个
	// 空哨兵，它不值得作为分页目标。把这个
	// 哨兵保留在上面的切片中，使完整读取保持逐字节一致，但这里
	// 只统计真实行数。
	totalLines := len(lines)
	if len(data) > 0 && data[len(data)-1] == '\n' {
		totalLines--
	}

	if args.Outline {
		// 大纲模式是文件级的"签名目录"：对 Go 文件，我们
		// 复用 repo-map 提取器列出顶层声明，而不是
		// 倾倒整个源码。行/偏移选择器在这里被有意
		// 忽略；调用方通过普通（outline=false）读取展开某个小节。
		// 非 Go 文件目前没有提取器，因此回退到普通读取，
		// 而不是返回空大纲或误导性的大纲。
		if strings.HasSuffix(strings.ToLower(path), ".go") {
			return clip(ctx, outlineGoFile(args.Path, path)), nil
		}
	}

	start, end := 1, len(lines)
	if args.Line > 0 {
		// 以间隔为中心的模式下：line 优先于 offset/limit。before/after 是
		// 彼此独立的每侧预算，钳制在 [0, readWindowCap] 内。
		before, after := args.Before, args.After
		if before < 0 {
			before = 0
		}
		if after < 0 {
			after = 0
		}
		if before > readWindowCap {
			before = readWindowCap
		}
		if after > readWindowCap {
			after = readWindowCap
		}
		start = args.Line - before
		if start < 1 {
			start = 1
		}
		end = args.Line + after
		if end > totalLines {
			end = totalLines
		}
		// 超出最后一行的目标没有窗口可显示；与
		// offset 路径越界时的行为保持一致。
		if start > totalLines {
			return "", nil
		}
	} else {
		// 旧的 offset/limit 路径：当 line == 0 时不变。
		start = args.Offset
		if start < 1 {
			start = 1
		}
		if start > len(lines) {
			return "", nil
		}
		end = len(lines)
		if args.Limit > 0 {
			// 计算结束位置时不把原始 limit 直接加到 start-1 上：巨大的
			// limit（如 9223372036854775807）会使 int 溢出，导致 end
			// 为负并在下面的切片处 panic。大于或等于
			// 剩余行数意味着"读到末尾"，这与未溢出算术产生的
			// 结果相同。
			remaining := len(lines) - start + 1 // start <= len(lines) here, so >= 1
			if args.Limit >= remaining {
				end = len(lines)
			} else {
				end = start - 1 + args.Limit
			}
		}
	}
	out := clip(ctx, strings.Join(lines[start-1:end], "\n"))

	// end 是最后显示的、基于 1 的行号；较小的真实总数表示
	// 调用方尚未到达文件末尾，因此引导它继续分页。
	if end < totalLines {
		out += fmt.Sprintf("\n[showing lines %d–%d of %d; re-call with offset/limit to page]", start, end, totalLines)
	}
	// 新鲜度账本（opt-in，由 app 注入）：读取成功后登记当前磁盘快照。
	if f := FileFreshnessFrom(ctx); f != nil {
		f.MarkRead(path)
	}
	return out, nil
}

// outlineGoFile 把 Go 文件渲染为紧凑的"签名目录"：每个
// 顶层声明一行，带上类别和基于 1 的行号。它复用
// repo-map 符号提取器，使"什么算符号"在
// read 工具和 repo 地图之间保持一致。display 是调用方提供的路径
// （仅用于头部）；disk 是待解析的绝对路径。
func outlineGoFile(display, disk string) string {
	syms := extractRepoSymbols(filepath.Dir(disk), []string{filepath.Base(disk)})
	var b strings.Builder
	fmt.Fprintf(&b, "outline: %s (%d symbols)", display, len(syms))
	if len(syms) == 0 {
		b.WriteString("\n  (no top-level declarations)")
		return b.String()
	}
	for _, s := range syms {
		name := s.Name
		if s.Receiver != "" {
			name = s.Receiver + "." + name
		}
		fmt.Fprintf(&b, "\n  %-7s %s:%d", s.Kind, name, s.Line)
	}
	return b.String()
}

// Meta 报告仅从文件路径派生的取代键。行/偏移
// 选择器被丢弃：对同一文件任意范围的后续读取，都会使
// 该文件先前的读取在上下文意义上失效。
func (readTool) Meta(args json.RawMessage, _ string, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ToolMeta{}
	}
	p := normalizeReadPath(a.Path)
	if p == "" {
		return ToolMeta{}
	}
	return ToolMeta{SupersedeKey: "read|" + p}
}

// readSelector 匹配追加到路径后的常见行范围后缀：
// ":10"、":10:20"、"#L10"、"#L10-L20" 及其逗号变体。
var readSelector = regexp.MustCompile(`(?::[0-9]+(?:[:,-][0-9]+)*|#[Ll]?[0-9]+(?:[-:][Ll]?[0-9]+)?)+$`)

// normalizeReadPath 清理读取路径并移除行选择器，使对
// 同一文件的两次读取无论范围如何都共用一个取代键。
func normalizeReadPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(p)
	p = readSelector.ReplaceAllString(p, "")
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return ""
	}
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}
