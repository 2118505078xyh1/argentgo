package tools

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrMalformedPatch 标记输入不是合法的 apply_patch 文本。
// 调用方可用 errors.Is 匹配；错误文本携带出错行的 1 基行号，
// 便于把问题定位回模型产出的原文。
var ErrMalformedPatch = errors.New("tools: malformed apply patch")

// PatchAction 表示 apply_patch 中的一个文件级动作。
// Kind 取值为 "add" / "delete" / "update"。
type PatchAction struct {
	Kind  string
	Path  string
	Hunks []PatchHunk
}

// PatchHunk 表示一个 @@ 段。
//
// 四个数字来自 "@@ -OldStart,OldLines +NewStart,NewLines @@"；
// 当 hunk 头只有 @@（可能附带定位提示）而没有数字时，它们全为 0。
// OldLines/NewLines 省略时按 1 计。
//
// Lines 保留每一行的原始前缀字符：' ' 表示上下文，'+' 表示新增，
// '-' 表示删除；完全为空的行表示空上下文。行尾的 '\r' 已被去掉，
// 以便 CRLF 与 LF 输入得到相同结果。前缀被刻意保留，使 OldText
// 与 NewText 能在同一份数据上分别推导出旧、新文本。
type PatchHunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []string
}

// patchHunkHeaderRE 匹配 "@@ -a[,b] +c[,d] @@" 形式的 hunk 头。
// 末尾不锚定，允许 @@ 之后继续携带定位用的上下文提示。
var patchHunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// malformedPatch 构造带 1 基行号的哨兵错误。
func malformedPatch(line int, format string, args ...any) error {
	return fmt.Errorf("%w: line %d: %s", ErrMalformedPatch, line, fmt.Sprintf(format, args...))
}

// splitPatchLines 按 '\n' 切分并去掉每行末尾的 '\r'，因此 CRLF 与 LF
// 得到完全相同的行内容。'\r' 与 '\n' 都是 ASCII 字节，绝不会出现在
// UTF-8 多字节 rune 内部，因此对 CJK 安全。结尾换行产生的单个空
// 哨兵元素会被丢弃；函数只构造新切片，从不改动传入的字符串。
func splitPatchLines(text string) []string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i, ln := range lines {
		lines[i] = strings.TrimSuffix(ln, "\r")
	}
	return lines
}

// ParseApplyPatch 把 apply_patch 文本解析成有序的 PatchAction 列表。
//
// 它是纯函数：不读取外部状态、不修改入参（Go 字符串本身不可变），
// 同一输入总是得到相同输出。识别的结构包括：
//
//	*** Begin Patch
//	*** Add File: <path> / *** Delete File: <path> / *** Update File: <path>
//	@@ [-a[,b] +c[,d] @@] [context]
//	<空格>上下文 / +新增 / -删除
//	*** End Patch
//
// 空行在结构层面被忽略；在 hunk 内部，完全为空的行按空上下文行处理。
// 行号语法存在但写错（例如 "@@ -bad +1 @@"）会被视为畸形输入。
func ParseApplyPatch(text string) ([]PatchAction, error) {
	lines := splitPatchLines(text)

	var (
		actions   []PatchAction
		beginSeen bool
		endSeen   bool
		// current 指向 actions 中正在填充的 action；-1 表示尚未打开文件。
		current = -1
	)

	for i, raw := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			// 结构层的空行忽略；内容层的空行在下面按内容行处理。
			continue
		}

		if !beginSeen {
			if trimmed != "*** Begin Patch" {
				return nil, malformedPatch(lineNo, "expected *** Begin Patch, got %q", raw)
			}
			beginSeen = true
			continue
		}

		if endSeen {
			// End 之后只允许空白（上面已 continue），其余一律拒绝。
			return nil, malformedPatch(lineNo, "unexpected content after *** End Patch: %q", raw)
		}

		switch {
		case trimmed == "*** End Patch":
			endSeen = true
			current = -1

		case strings.HasPrefix(trimmed, "*** "):
			kind, path, ok := parsePatchFileDirective(trimmed)
			if !ok {
				return nil, malformedPatch(lineNo, "unknown directive: %q", raw)
			}
			if path == "" {
				return nil, malformedPatch(lineNo, "empty path in directive: %q", raw)
			}
			actions = append(actions, PatchAction{Kind: kind, Path: path})
			current = len(actions) - 1

		case strings.HasPrefix(trimmed, "@@"):
			if current < 0 {
				return nil, malformedPatch(lineNo, "@@ hunk outside of a file directive")
			}
			hunk, err := parsePatchHunkHeader(trimmed, lineNo)
			if err != nil {
				return nil, err
			}
			actions[current].Hunks = append(actions[current].Hunks, hunk)

		case isPatchContentLine(raw):
			if current < 0 {
				return nil, malformedPatch(lineNo, "patch content outside of a file directive")
			}
			hunks := &actions[current].Hunks
			if len(*hunks) == 0 {
				// @@ 之前出现的内容行放进一个隐式 hunk（行号全 0）。
				// Add File 的文件体通常就是这样写的。
				*hunks = append(*hunks, PatchHunk{})
			}
			last := len(*hunks) - 1
			(*hunks)[last].Lines = append((*hunks)[last].Lines, raw)

		default:
			return nil, malformedPatch(lineNo, "unexpected line: %q", raw)
		}
	}

	if !beginSeen {
		return nil, malformedPatch(1, "missing *** Begin Patch")
	}
	if !endSeen {
		return nil, malformedPatch(len(lines)+1, "missing *** End Patch")
	}
	return actions, nil
}

// parsePatchFileDirective 解析三种文件指令，返回 Kind 与去空白后的路径。
func parsePatchFileDirective(line string) (kind, path string, ok bool) {
	switch {
	case strings.HasPrefix(line, "*** Add File:"):
		return "add", strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:")), true
	case strings.HasPrefix(line, "*** Delete File:"):
		return "delete", strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:")), true
	case strings.HasPrefix(line, "*** Update File:"):
		return "update", strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:")), true
	default:
		return "", "", false
	}
}

// parsePatchHunkHeader 解析 @@ 头。支持带行号的
// "@@ -a[,b] +c[,d] @@" 与仅作定位提示的 "@@ context"。
// 当行号看似存在却写错时返回带行号的 ErrMalformedPatch。
func parsePatchHunkHeader(line string, lineNo int) (PatchHunk, error) {
	if m := patchHunkHeaderRE.FindStringSubmatch(line); m != nil {
		oldStart, _ := strconv.Atoi(m[1])
		oldLines := 1
		if m[2] != "" {
			oldLines, _ = strconv.Atoi(m[2])
		}
		newStart, _ := strconv.Atoi(m[3])
		newLines := 1
		if m[4] != "" {
			newLines, _ = strconv.Atoi(m[4])
		}
		return PatchHunk{OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}, nil
	}

	// 未匹配数字形式：若剩余部分以 '-'/'+' 开头，说明作者试图写行号
	// 却写错了，判为畸形；否则当作纯上下文提示，行号留 0。
	remainder := strings.TrimSpace(strings.TrimPrefix(line, "@@"))
	if strings.HasPrefix(remainder, "-") || strings.HasPrefix(remainder, "+") {
		return PatchHunk{}, malformedPatch(lineNo, "malformed hunk header: %q", line)
	}
	return PatchHunk{}, nil
}

// isPatchContentLine 判断一行是否是 hunk 内容行：以 ' '（上下文）、
// '+'（新增）、'-'（删除）开头，或完全为空（空上下文）。
func isPatchContentLine(line string) bool {
	if line == "" {
		return true
	}
	switch line[0] {
	case ' ', '+', '-':
		return true
	default:
		return false
	}
}

// OldText 返回该动作所有 hunk 中属于旧文本的行（上下文与删除行，
// 去掉前缀）按顺序以 "\n" 连接的结果；新增行被忽略。
// 多个 hunk 会被扁平拼接，并不重建跨越 hunk 间隙的完整文件。
func (a PatchAction) OldText() string {
	return a.hunkText(false)
}

// NewText 返回该动作所有 hunk 中属于新文本的行（上下文与新增行，
// 去掉前缀）按顺序以 "\n" 连接的结果；删除行被忽略。
// 多个 hunk 会被扁平拼接，并不重建跨越 hunk 间隙的完整文件。
func (a PatchAction) NewText() string {
	return a.hunkText(true)
}

func (a PatchAction) hunkText(wantNew bool) string {
	// 逐 hunk 推导再以 "\n" 拼接，结果与把所有行摊平后拼接完全一致
	// （hunk 之间的边界只是同一个 "\n" 分隔符）。
	parts := make([]string, 0, len(a.Hunks))
	for _, h := range a.Hunks {
		parts = append(parts, patchLinesText(h.Lines, wantNew))
	}
	return strings.Join(parts, "\n")
}

// oldText 返回该 hunk 中属于旧文本的行（上下文与删除行，
// 去掉前缀）按顺序以 "\n" 连接的结果；新增行被忽略。
// 与 PatchAction.OldText 不同，它只作用于单个 hunk，供逐 hunk 应用。
func (h PatchHunk) oldText() string {
	return patchLinesText(h.Lines, false)
}

// newText 返回该 hunk 中属于新文本的行（上下文与新增行，
// 去掉前缀）按顺序以 "\n" 连接的结果；删除行被忽略。
// 与 PatchAction.NewText 不同，它只作用于单个 hunk，供逐 hunk 应用。
func (h PatchHunk) newText() string {
	return patchLinesText(h.Lines, true)
}

// OldText 是 oldText 的导出别名，便于需要逐 hunk 取旧文本的调用方使用。
func (h PatchHunk) OldText() string { return h.oldText() }

// NewText 是 newText 的导出别名，便于需要逐 hunk 取新文本的调用方使用。
func (h PatchHunk) NewText() string { return h.newText() }

// patchLinesText 是 PatchAction.hunkText 与 PatchHunk.oldText/newText
// 共用的前缀剥离逻辑：' ' 同时属于新旧文本，'+' 只属于新文本，
// '-' 只属于旧文本；完全为空的行按空上下文处理。
func patchLinesText(lines []string, wantNew bool) string {
	parts := make([]string, 0, len(lines))
	for _, ln := range lines {
		if len(ln) == 0 {
			parts = append(parts, "")
			continue
		}
		switch ln[0] {
		case ' ':
			// 上下文同时属于旧文本与新文本。
			parts = append(parts, ln[1:])
		case '+':
			if wantNew {
				parts = append(parts, ln[1:])
			}
		case '-':
			if !wantNew {
				parts = append(parts, ln[1:])
			}
		}
	}
	return strings.Join(parts, "\n")
}
