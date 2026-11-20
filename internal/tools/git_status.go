package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ---- git_status ----

// gitStatusCommand 构建只读的 git 调用。它是一个变量，
// 以便测试可以替换为一个确定性的命令。
var gitStatusCommand = func(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", "status", "--short", "--branch")
	cmd.Dir = dir
	return cmd
}

type gitStatusTool struct{}

func (gitStatusTool) Name() string { return "git_status" }
func (gitStatusTool) Description() string {
	return "Show the working tree status (git status --short --branch). Read-only."
}
func (gitStatusTool) Mutating() bool { return false }
func (gitStatusTool) Schema() string { return `{"type":"object","properties":{}}` }

func (gitStatusTool) Run(ctx context.Context, root string, _ json.RawMessage) (string, error) {
	timeout := clampTimeout("git_status", 0, maxTimeout(ctx))
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	output, err := gitStatusCommand(runCtx, root).CombinedOutput()
	text := strings.TrimSpace(string(output))
	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("git_status: timed out after %s", timeout)
	}
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("git_status: %w", err)
		}
		return clip(ctx, text), fmt.Errorf("git_status: %w", err)
	}
	if text == "" {
		return "(clean)", nil
	}
	return clip(ctx, text), nil
}

// parseDirtyFiles 解析 `git status --porcelain` 的输出，返回稳定有序的变更文件路径。
// 它会取重命名/复制条目的新路径，还原 git 对特殊路径的引号包裹与 C 风格转义
// （包括 CJK 等非 ASCII 字节的八进制表示），去重后按字典序排序。
func parseDirtyFiles(porcelainOutput string) []string {
	seen := make(map[string]struct{})
	var files []string
	for _, line := range strings.Split(porcelainOutput, "\n") {
		// porcelain 每行的格式为“两位状态码 + 空格 + 路径”，至少 4 个字符；
		// “## ”开头的是 --branch 附加的分支信息，不属于变更文件。
		if len(line) < 4 || strings.HasPrefix(line, "## ") {
			continue
		}
		raw := strings.TrimSpace(line[3:])
		if raw == "" {
			continue
		}
		path := unquoteGitPath(renameTarget(raw))
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

// renameTarget 取出重命名/复制条目中箭头右侧的新路径。
// 扫描时会跳过引号内的内容，避免把文件名里字面出现的“ -> ”误当作分隔符。
func renameTarget(raw string) string {
	inQuote, escaped := false, false
	for i := 0; i+3 < len(raw); i++ {
		c := raw[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote && c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && c == ' ' && raw[i:i+4] == " -> " {
			return raw[i+4:]
		}
	}
	return raw
}

// unquoteGitPath 还原 git 在 porcelain 输出里对含空格、引号或非 ASCII 字符的
// 路径所做的引号包裹与 C 风格转义；未被包裹的路径原样返回。八进制转义按字节
// 收集后再组合，因此 `\344\270\255` 能正确还原成 UTF-8 的“中”。
func unquoteGitPath(path string) string {
	if len(path) < 2 || path[0] != '"' || path[len(path)-1] != '"' {
		return path
	}
	inner := path[1 : len(path)-1]
	out := make([]byte, 0, len(inner))
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '\\' {
			out = append(out, c)
			continue
		}
		i++
		if i >= len(inner) {
			break
		}
		switch inner[i] {
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case 'r':
			out = append(out, '\r')
		case 'a':
			out = append(out, '\a')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'v':
			out = append(out, '\v')
		case '\\':
			out = append(out, '\\')
		case '"':
			out = append(out, '"')
		default:
			if inner[i] >= '0' && inner[i] <= '7' {
				value := int(inner[i] - '0')
				for j := 0; j < 2 && i+1 < len(inner) && inner[i+1] >= '0' && inner[i+1] <= '7'; j++ {
					i++
					value = value*8 + int(inner[i]-'0')
				}
				out = append(out, byte(value))
			} else {
				// 未知转义：保守地保留反斜杠，避免丢失路径信息。
				out = append(out, '\\', inner[i])
			}
		}
	}
	return string(out)
}

// gitDiffCommand 构造供命令执行层消费的 git diff 参数（不含可执行文件名 git，
// 调用方可直接 `exec.CommandContext(ctx, "git", gitDiffCommand(...)...)`）。
// 有提交时单条 `git diff HEAD -- files` 即可覆盖暂存与未暂存的改动；
// 无提交（空仓库）时本函数返回暂存区对比 `git diff --cached -- files`，
// 调用方还需执行 gitWorktreeDiffCommand 返回的工作区对比，两者合起来覆盖全部改动。
func gitDiffCommand(hasCommits bool, files []string) []string {
	args := []string{"diff"}
	if hasCommits {
		args = append(args, "HEAD")
	} else {
		args = append(args, "--cached")
	}
	args = append(args, "--")
	return append(args, files...)
}

// gitWorktreeDiffCommand 构造空仓库下的工作区对比参数 `git diff -- files`。
// 它与 gitDiffCommand(false, files) 的暂存区对比合用，覆盖暂存与未暂存改动。
func gitWorktreeDiffCommand(files []string) []string {
	args := []string{"diff", "--"}
	return append(args, files...)
}
