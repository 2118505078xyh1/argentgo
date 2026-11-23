package memory

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// 工具触及路径后的「嵌套指令文件」提醒。
//
// 多包仓库里模型读了 src/parser/x.go 却不知道 src/parser/AGENTS.md 的局部约定，
// 容易改错。把全文塞进 system 会破坏缓存前缀，因此这里复用 LoadForDirs 做发现，
// 却只取 Doc.Path（相对路径）生成一条有界提醒，供调用方经 steering 在下一轮追加。
// 绝不注入正文、绝不改 system prompt。

const (
	// NestedHintMaxPaths 是单条提醒中最多列出的文件路径数。
	NestedHintMaxPaths = 8
	// NestedHintMaxBytes 是单条提醒的字节上限（UTF-8 字节，保守代理 token 预算）。
	NestedHintMaxBytes = 512
	// TouchedPathLimit 是调用方在一次运行内收集的触及路径上限。
	TouchedPathLimit = 200
)

const (
	nestedHintHeader = "<system-reminder>These AGENTS.md files apply to paths you just used. Read them before changing anything in those folders: "
	nestedHintFooter = "</system-reminder>"
	nestedHintSep    = ", "
)

// NestedInstructionHint 发现 cwd 下与 dirs 相关的嵌套指令文件，并只用它们的
// 路径生成一条有界提醒。无发现时返回空串。dirs 可以是目录也可以是文件路径；
// LoadForDirs 会逐级向上探测祖先，因此传文件路径同样能发现其所在目录的规则。
func NestedInstructionHint(cwd string, dirs []string) string {
	docs := LoadForDirs(cwd, dirs)
	if len(docs) == 0 {
		return ""
	}

	paths := make([]string, 0, len(docs))
	for _, doc := range docs {
		rel, err := filepath.Rel(cwd, doc.Path)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			rel = filepath.Base(doc.Path)
		}
		rel = filepath.ToSlash(rel)
		paths = append(paths, rel)
		if len(paths) >= NestedHintMaxPaths {
			break
		}
	}

	pathBudget := NestedHintMaxBytes - len(nestedHintHeader) - len(nestedHintFooter)
	if pathBudget <= 0 {
		return ""
	}
	var parts []string
	used := 0
	for _, p := range paths {
		add := len(p)
		if used > 0 {
			add += len(nestedHintSep)
		}
		if used+add > pathBudget {
			remaining := pathBudget - used
			if used > 0 {
				remaining -= len(nestedHintSep)
			}
			if remaining > 0 {
				if piece := clipHintRunes(p, remaining); piece != "" {
					parts = append(parts, piece)
				}
			}
			break
		}
		parts = append(parts, p)
		used += add
	}
	if len(parts) == 0 {
		return ""
	}
	return nestedHintHeader + strings.Join(parts, nestedHintSep) + nestedHintFooter
}

// clipHintRunes 按 rune 边界把 s 截到至多 limit 字节，避免在 CJK/emoji 中间切断。
func clipHintRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	k := limit
	for k > 0 && !utf8.RuneStart(s[k]) {
		k--
	}
	return s[:k]
}
