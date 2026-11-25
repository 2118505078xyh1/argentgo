package tools

import (
	"path/filepath"
	"strings"

	"example.com/argentgo/internal/security"
)

// ignoreBOM 是首行可能携带的 UTF-8 字节序标记。它属于编码元数据，
// 必须在解析模式前剥离，否则首条模式会带上不可见前缀而永不匹配。
const ignoreBOM = "\xef\xbb\xbf"

// isIgnoredPath 按 gitignore 风格模式判断相对路径 relPath 是否被忽略。
//
// 匹配复用 security.GlobMatch，因此继承它的全部语义：`*` 只在单个
// 路径段内匹配，`**` 跨段匹配，`?` 匹配单个字符；不含斜杠的模式会在
// 每个尾部段上尝试，因此 `*.env` 能命中 `project/src/.env`。反斜杠被
// 当作分隔符；大小写一律不敏感（与仓库既有的 .argentignore 处理及安全
// 策略保持一致）；CJK 字符按 rune 比较，不会在 UTF-8 字节中间被截断。
//
// 行处理遵循 .argentignore 约定：跳过空行与 `#` 注释，剥离首行 BOM，
// 前导 `!` 表示取反（重新包含），首尾 `/` 会被裁剪，裸 `!` 被丢弃。
// 最后一条匹配的模式决定结果，与 gitignore 的"后者胜出"规则一致。
//
// 该函数是纯函数：不访问文件系统，也不修改传入的 patterns。
func isIgnoredPath(relPath string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}

	rel := filepath.ToSlash(filepath.Clean(relPath))
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." || rel == "" {
		return false
	}

	ignored := false
	for i, raw := range patterns {
		line := strings.TrimSpace(raw)
		if i == 0 {
			line = strings.TrimPrefix(line, ignoreBOM)
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimSpace(line[1:])
		}
		line = strings.Trim(line, "/")
		if line == "" {
			continue
		}
		glob := strings.ReplaceAll(filepath.ToSlash(line), `\`, "/")
		if security.GlobMatch(glob, rel) {
			ignored = !negated
		}
	}
	return ignored
}
