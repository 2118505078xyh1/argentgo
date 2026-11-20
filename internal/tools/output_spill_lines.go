package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 对溢出产出文件按行恢复。这是一个纯辅助工具：它不写
// 任何内容、不做假脱机、不查询工具注册表。它存在的目的是让
// 模型（或未来的接线点）能按行范围继续读取已被溢出、截断的
// 结果，而不必重新读取整个产出文件。
//
// 行号和尾随换行的计算刻意与 read 工具的 offset/limit 路径保持一致
// （见 tools_builtin_read.go）："基于 1 的首行"、当 endLine <= 0 时
// 表示无界结尾，以及尾随换行是
// 终止符而非幻影般的额外一行。

var (
	// errSpillInvalidPath 标记一个不是可用溢出产出
	// 位置（为空、含遍历、目录或符号链接）的路径。
	errSpillInvalidPath = errors.New("tools: invalid spill artifact path")
	// errSpillNotArtifact 标记不带有本包写出的
	// 规范产出文件名的路径。
	errSpillNotArtifact = errors.New("tools: not a spill artifact")
)

// ReadSpillRange 从溢出产出中返回含端点的、基于 1 的行范围
// [startLine, endLine]，以及该产出文件的总行数，以便
// 调用方决定是否继续分页。
//
// 语义（与 read 工具一致）：
//   - startLine < 1 被钳制为 1；超过最后真实行的 startLine
//     返回空内容（而非错误）；
//   - endLine <= 0 表示"到文件末尾"；超过最后行的正 endLine
//     行号会被钳制到该值；
//   - 有界读取没有尾随换行，而读到 EOF 的读取保留
//     文件原有的尾随换行（使完整读取逐字节一致）；
//   - startLine > endLine 是空范围。
//
// 路径在任何 I/O 之前都会先被校验为溢出产出：它必须有
// 规范的产出文件名，不含 ".." 遍历段，并解析为
// 普通的非符号链接文件。这些条件共同保证任意文件（会话
// 密钥、配置、/etc/passwd 等）都无法通过这个辅助工具读取。
func ReadSpillRange(path string, startLine, endLine int) (content string, totalLines int, err error) {
	// 从产出自身的目录推导校验根目录意味着该产出必须是其溢出目录的
	// 直接、扁平子项 —— 这正是 writeSpillArtifact 产出的
	// 布局（filepath.Join(dir, name)）。
	return readSpillRangeInDir(filepath.Dir(path), path, startLine, endLine)
}

// readSpillRangeInDir 是感知目录的核心。它复用 spillArtifactAbsInDir ——
// 即 isOwnSpillResult 用来识别我们产出的同一套路径校验 ——
// 因此路径逃逸或形似的文件名永远到不了文件读取这一步。
// 让 dir 显式传入，使它在调用方有已配置的溢出目录需要传入时，
// 成为自然的接线点。
func readSpillRangeInDir(dir, path string, startLine, endLine int) (content string, totalLines int, err error) {
	if strings.TrimSpace(path) == "" {
		return "", 0, fmt.Errorf("%w: empty path", errSpillInvalidPath)
	}
	if e := rejectSpillTraversal(path); e != nil {
		return "", 0, e
	}

	abs, ok := spillArtifactAbsInDir(dir, path)
	if !ok {
		return "", 0, fmt.Errorf("%w: %q", errSpillNotArtifact, path)
	}
	// spillArtifactAbsInDir 使用 os.Stat（会跟随符号链接）。溢出文件
	// 始终以 O_EXCL 创建为普通文件，因此要拒绝那些否则可能
	// 指向溢出目录外任意目标的符号链接。
	info, err := os.Lstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", 0, fmt.Errorf("%w: %q is not a regular spill file", errSpillInvalidPath, path)
	}

	return readSpillRangeAt(abs, startLine, endLine)
}

// rejectSpillTraversal 在 filepath.Abs 和
// filepath.Clean 隐藏 ".." 路径段之前将其拒绝。否则 "../../secret/tool-output-<id>.log"
// 会被清理成溢出目录之外的路径。
func rejectSpillTraversal(path string) error {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == ".." {
			return fmt.Errorf("%w: traversal segment in %q", errSpillInvalidPath, path)
		}
	}
	return nil
}

// readSpillRangeAt 对已校验的绝对产出路径执行实际的有界读取。
// 行切分是 rune 安全的，因为 '\n' (0x0A) 绝不会出现在
// 多字节 UTF-8 rune 内部；utf8.Valid 会拒绝任何
// 意外携带损坏字节的产出。
func readSpillRangeAt(abs string, startLine, endLine int) (content string, totalLines int, err error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", 0, fmt.Errorf("read spill artifact: %w", err)
	}
	if !utf8.Valid(data) {
		return "", 0, fmt.Errorf("spill artifact %q is not UTF-8 text", abs)
	}

	lines, total := splitSpillLines(data)
	if total == 0 { // empty artifact
		return "", 0, nil
	}

	start := startLine
	if start < 1 {
		start = 1
	}
	if start > total {
		return "", total, nil
	}

	var end int
	if endLine <= 0 {
		// 一直读到 EOF，包括尾随哨兵，使完整读取
		// 与原始产出逐字节一致。
		end = len(lines)
	} else {
		end = endLine
		if end > total {
			end = total
		}
	}
	if end < start {
		return "", total, nil
	}

	return strings.Join(lines[start-1:end], "\n"), total, nil
}

// splitSpillLines 沿用 read 工具的计算方式：strings.Split 会在
// 末尾换行后保留一个空哨兵，它用于终止最后一行，而
// 不是开启一个新的真实行。它保留在溢出辅助工具中（而非导入
// read 工具的内部实现），使两个包保持解耦。
func splitSpillLines(data []byte) (lines []string, totalLines int) {
	if len(data) == 0 {
		return nil, 0
	}
	lines = strings.Split(string(data), "\n")
	totalLines = len(lines)
	if data[len(data)-1] == '\n' {
		totalLines--
	}
	return lines, totalLines
}
