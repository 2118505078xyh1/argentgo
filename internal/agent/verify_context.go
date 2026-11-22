package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

const (
	maxSourceAnchors   = 8
	maxSourceFileBytes = 1 << 20 // 每个文件 1 MiB
	maxSourceLineBytes = 240     // 每条输出的源代码行
	sourceTruncMark    = "..."

	// sourceAnchorPattern 匹配工具输出中的 file:line 引用。
	// 第 1 组是可选的 Windows 盘符前缀（"C:\"），第 2 组是路径，
	// 第 3 组是行号，第 4 组是可选的列号。
	// 路径字符类刻意允许大多数非空白字符（例如 Windows RUNNER~1 临时目录中的 "~"，空格被排除），
	// 因为 CI runner 上的绝对临时路径包含狭窄字符类会丢弃的字符，那会破坏锚定。
	// 排除冒号，使盘符前缀和结尾的 :行号 保持无歧义。
	sourceAnchorPattern = `([A-Za-z]:[\\/])?([^\s:]+):([0-9]{1,6})(?::([0-9]{1,6}))?`
)

// sourceAnchorRE 提升为包级变量：AnchorSourceLines 处于失败反馈热路径，
// 每次调用重新编译正则会带来无谓分配。
var sourceAnchorRE = regexp.MustCompile(sourceAnchorPattern)

type sourceAnchor struct {
	name string // 工具输出中出现的原样路径
	path string // 解析后、限定在 root 内的绝对路径
	line int
}

// AnchorSourceLines 把输出中的 file:line 引用转换为简短的源码窗口，并在有问题的行上标记 █。
// 它与 VerificationFeedback 组合使用（而非取代它）：调用方把返回的文本块追加到反馈文本中，
// 使 lint/测试失败能显示出问题的源码。
//
// 该函数从不写磁盘，也从不越出 root：root 之外的绝对路径以及任何含 .. 越界的路径都会被忽略。
// 文件读取是有界的（每个文件 1 MiB，最多 maxSourceAnchors 个锚点），二进制/NUL 文件会被跳过，
// 每一行输出都在 UTF-8 rune 边界处裁剪，并经 llm.RedactText 脱敏。输出是一系列如下形式的文本块：
//
//	fname:12:
//	10| 前一行
//	11| 前一行
//	█12| 有问题的行
//	13| 后一行
//
// 如果没有找到可用的锚点，或每个被引用的文件都不可读，则返回 ""，使调用方保持反馈不变。
func AnchorSourceLines(output, root string, pad int) string {
	if strings.TrimSpace(output) == "" {
		return ""
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	if pad < 0 {
		pad = 0
	}

	seen := make(map[string]bool)
	anchors := make([]sourceAnchor, 0, maxSourceAnchors)
	// 手动逐个匹配，收满 maxSourceAnchors 个有效锚点即停；避免
	// FindAllStringSubmatch(-1) 为整段输出预先分配全部匹配结果。
	rest := output
	for len(rest) > 0 && len(anchors) < maxSourceAnchors {
		loc := sourceAnchorRE.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		name := ""
		if loc[2] >= 0 { // 可选盘符前缀（第 1 组）
			name += rest[loc[2]:loc[3]]
		}
		name += rest[loc[4]:loc[5]] // 路径（第 2 组，必填）
		if !looksLikeSourcePath(name) {
			rest = rest[loc[1]:]
			continue
		}
		line, err := strconv.Atoi(rest[loc[6]:loc[7]]) // 行号（第 3 组，必填）
		if err == nil && line >= 1 {
			if path, ok := resolveSourcePath(name, rootAbs); ok {
				key := path + "\x00" + strconv.Itoa(line)
				if !seen[key] {
					seen[key] = true
					anchors = append(anchors, sourceAnchor{name: name, path: path, line: line})
				}
			}
		}
		rest = rest[loc[1]:]
	}
	if len(anchors) == 0 {
		return ""
	}

	var b strings.Builder
	for _, a := range anchors {
		if renderSourceWindow(&b, a, pad) {
			b.WriteByte('\n')
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// looksLikeSourcePath 过滤掉非路径的标记，例如 "ERROR:404"。真实的文件引用总带有分隔符或文件扩展名。
func looksLikeSourcePath(s string) bool {
	return strings.ContainsAny(s, "./\\")
}

// resolveSourcePath 把 raw 限定在 root 内。相对路径会拼接到 root 上，并用 filepath.Rel 重新检查；
// 绝对路径只有在已位于 root 内时才会被接受。任何 .. 越界或越出 root 的结果都会被拒绝。
// 返回的路径是绝对路径，且始终位于 root 之下。
func resolveSourcePath(raw, root string) (string, bool) {
	cleaned := filepath.Clean(raw)
	if cleaned == "" || cleaned == "." {
		return "", false
	}
	if filepath.IsAbs(cleaned) {
		rel, err := filepath.Rel(root, cleaned)
		if err != nil || isOutsideRoot(rel) {
			return "", false
		}
		return filepath.Join(root, rel), true
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	joined := filepath.Join(root, cleaned)
	rel, err := filepath.Rel(root, joined)
	if err != nil || isOutsideRoot(rel) {
		return "", false
	}
	return joined, true
}

func isOutsideRoot(rel string) bool {
	return rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel)
}

// renderSourceWindow 向 b 追加一个锚定窗口。它报告是否输出了窗口
// （文件不可读或行号超出文件末尾时为 false）。
func renderSourceWindow(b *strings.Builder, a sourceAnchor, pad int) bool {
	lines, ok := readSourceLines(a.path)
	if !ok || a.line > len(lines) {
		return false
	}
	start := a.line - pad
	if start < 1 {
		start = 1
	}
	end := a.line + pad
	if end > len(lines) {
		end = len(lines)
	}
	width := len(strconv.Itoa(end))

	b.WriteString(llm.RedactText(a.name))
	b.WriteByte(':')
	b.WriteString(strconv.Itoa(a.line))
	b.WriteString(":\n")
	for n := start; n <= end; n++ {
		marker := " "
		if n == a.line {
			marker = "█"
		}
		fmt.Fprintf(b, "%s%*d| %s\n", marker, width, n, clipSourceLine(lines[n-1]))
	}
	return true
}

// readSourceLines 读取一个有界的纯文本源文件。NUL 字节、无效 UTF-8、空文件以及大于
// maxSourceFileBytes 的文件都会被跳过。
func readSourceLines(path string) ([]string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxSourceFileBytes+1))
	if err != nil || len(data) > maxSourceFileBytes || len(data) == 0 {
		return nil, false
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return nil, false
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	// 末尾换行只是终止最后一行真实内容，并不是要在窗口中显示的额外空行。
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, true
}

// clipSourceLine 先脱敏再限制一行源代码的长度，在 rune 边界处裁剪，
// 使输出的文本始终是合法 UTF-8。
func clipSourceLine(line string) string {
	line = llm.RedactText(line)
	if len(line) <= maxSourceLineBytes {
		return line
	}
	cut := maxSourceLineBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + sourceTruncMark
}
