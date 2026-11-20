package memory

// 分层、建议性的 .argentignore 发现。
//
// 与 memory.go 中的指令文件（它们被注入到
// 提示词）不同，这些文件只说明调用方可以选择将什么排除在
// 提示词之外。这镜像了 Cursor 的 .cursorignore：它是尽力而为的便利
// 手段，用于把明显无关的路径挡在上下文之外，并且明确
// 不是安全边界。绝不要用 IgnoreRules.Ignored 来决定是否
// 可以读取或执行不可信内容 —— 那应使用 internal/security。

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// IgnoreFileName 是 LoadIgnore 发现的每目录排除文件。
const IgnoreFileName = ".argentignore"

// maxIgnoreFileBytes 限制单个 .argentignore 文件的读取量。忽略
// 文件是建议性的，因此超大文件会被跳过，而不是导致
// 整个遍历失败。
const maxIgnoreFileBytes = 1 << 20

// pattern 是一条解析后的 .argentignore 行。
//
// 匹配复用 security.GlobMatch：memory 已经依赖
// internal/security（见 bank.go），因此没有导入循环；复用它
// 还能使 glob 语义与策略匹配器完全一致（`*` 在
// 单个段内、`**` 跨段、`?` 匹配一个字符，外加尾部匹配）。
// 这里只增加了 gitignore 风格的行处理（`#`、`!`、空行）。
type pattern struct {
	glob    string
	negated bool
	source  string // file the line came from, kept for diagnostics
}

// IgnoreRules 是 LoadIgnore 发现的 .argentignore 模式的扁平集合，
// 按父目录优先排序，使最后一条匹配模式
// （最具体的那条）胜出。
//
// 尽力而为且仅作建议，类似 Cursor 的 .cursorignore：不是安全
// 边界。漏报只意味着多余的文本进入了上下文。
type IgnoreRules struct {
	patterns []pattern
}

// LoadIgnore 从 dir 向上遍历，收集 .argentignore 文件。发现顺序是
// 子 -> 父，但规则按父 -> 子存储，因此嵌套
// 目录可以用 `!` 覆盖祖先的规则。
//
// 每行是一个 glob；空行和以 `#` 开头的行被忽略，
// 前导 `!` 重新包含先前模式排除的路径。末尾的
// `/` 会被去掉，因此像 `build/` 这样的目录模式也能匹配其
// 内容。模式在匹配前被规范化为正斜杠 glob。
//
// 只有无效目录（stat/绝对路径失败）才是错误。无法读取、
// 超大或名称错误的条目会被跳过：发现是尽力而为的，
// 绝不能破坏提示词的构建。
func LoadIgnore(dir string) (*IgnoreRules, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory: %s is not a directory", abs)
	}

	// 先按子 -> 父遍历，再反转为父 -> 子的优先级。
	// maxDepth 与 memory.go 中向上遍历使用的守卫相同。
	var found []string
	current := filepath.Clean(abs)
	for depth := 0; depth < maxDepth; depth++ {
		path := filepath.Join(current, IgnoreFileName)
		if st, statErr := os.Stat(path); statErr == nil && !st.IsDir() {
			found = append(found, path)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	rules := &IgnoreRules{}
	for i := len(found) - 1; i >= 0; i-- {
		f, err := os.Open(found[i])
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(f, maxIgnoreFileBytes+1))
		_ = f.Close()
		if err != nil || len(raw) > maxIgnoreFileBytes {
			continue
		}
		rules.patterns = append(rules.patterns, parseIgnorePatterns(string(raw), found[i])...)
	}
	return rules, nil
}

// normalizeIgnoreGlob 将裁剪后的 .argentignore 行转换为规范
// glob 形式，供加载器和诊断共用。.argentignore 采用
// gitignore 风格，其中 `/` 在所有平台上都是分隔符，因此反斜杠
// 会被无条件规范化：filepath.ToSlash 只转换*原生*
// 分隔符，在 Linux 上会保留 `\`（那里它是合法的文件名
// 字节）。security.GlobMatch 同样在两边把 `\` 变成 `/`，因此这
// 使存储的 glob 与各地匹配时实际使用的字符串完全一致。
func normalizeIgnoreGlob(line string) string {
	return strings.ReplaceAll(filepath.ToSlash(line), `\`, "/")
}

// parseIgnorePatterns 将一个文件的文本转换为有序模式。
func parseIgnorePatterns(text, source string) []pattern {
	var out []pattern
	// 前导 UTF-8 BOM 是编码元数据，不属于第一行。
	// 指令文件加载器在 readInstructionFile 中去掉它；忽略
	// 文件也必须这样做，否则第一个模式会带有 BOM 前缀
	// （从而永不匹配），而带 BOM 前缀的注释会被当作模式读取。
	text = strings.TrimPrefix(text, bom)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := false
		if strings.HasPrefix(line, "!") {
			negated = true
			line = strings.TrimSpace(line[1:])
		}
		line = strings.Trim(line, "/")
		if line == "" {
			continue
		}
		out = append(out, pattern{
			glob:    normalizeIgnoreGlob(line),
			negated: negated,
			source:  source,
		})
	}
	return out
}

// Ignored 报告 relPath 是否被最后一条匹配的模式排除。
// relPath 相对于传给 LoadIgnore 的目录解释；两种
// 分隔符都被接受，因为 filepath.ToSlash 会规范化 Windows 路径。
// nil 规则集或没有模式的规则集不排除任何内容。
//
// 匹配在所有平台上都不区分大小写，继承 GlobMatch 的
// 小写化处理（与安全策略行为一致），因此结果不会
// 在 Windows 和 Linux 之间发生变化。
func (r *IgnoreRules) Ignored(relPath string) bool {
	if r == nil || len(r.patterns) == 0 {
		return false
	}
	rel := filepath.ToSlash(filepath.Clean(relPath))
	rel = strings.TrimPrefix(rel, "./")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." || rel == "" {
		return false
	}

	ignored := false
	for _, p := range r.patterns {
		if security.GlobMatch(p.glob, rel) {
			ignored = !p.negated
		}
	}
	return ignored
}

// IgnoreRuleInfo 是一条解析后的忽略模式及其易读的
// 说明，由 DescribeIgnoreRules 产生。它是纯诊断值：
// 不执行磁盘 I/O，也不向提示词注入任何内容。
type IgnoreRuleInfo struct {
	// Pattern 是传给 DescribeIgnoreRules 的原始行。
	Pattern string
	// Glob 是实际用于匹配的规范化模式：去掉前导
	// `!`、裁剪首尾 `/` 并将分隔符改为正斜杠，
	// 与 parseIgnorePatterns 产生的结果完全一致。
	Glob string
	// Negated 报告该行是否是 `!` 重新包含，而非
	// exclusion.
	Negated bool
	// Description 是对模式作用（字面路径还是 glob 元字符、
	// 排除还是重新包含）的简短语义说明。
	Description string
	// SensitiveRisk 报告某条排除模式是否可能意外地也
	// 匹配敏感路径（凭据或 agent 元数据位置）。否定
	// 模式是重新包含，因此从不带此风险。
	SensitiveRisk bool
	// RiskReason 列出该模式匹配到的代表性敏感探测路径；
	// 当 SensitiveRisk 为 false 时为空。
	RiskReason string
}

// sensitiveProbes 是仅用于诊断的代表性路径。某条规则
// 若匹配其中任一路径，就会被标记为可能的意外排除。该
// 集合混合了常见的凭据/元数据位置和 CJK 名称，使检查也
// 覆盖非 ASCII 路径。这是尽力而为的警告，不是安全
// 边界：它刻意保持小而可解释，而非穷尽。
var sensitiveProbes = []string{
	".ssh",
	".ssh/id_rsa",
	".aws/credentials",
	".env",
	".git/config",
	".argent",
	"密钥/私钥.pem",
	"凭证.txt",
}

// DescribeIgnoreRules 解析原始的 .argentignore 风格模式行，并返回
// 每个有效模式对应一个 IgnoreRuleInfo，按输入顺序排列。它是
// parseIgnorePatterns 的纯诊断伴侣：行处理（BOM、注释、
// 空行、裸 `!`、前导 `/`、分隔符规范化）完全相同，因此报告的
// Glob 正是 Ignored 会匹配的字符串。
//
// 空行、注释和裸 `!` 行会被跳过，与加载器一致；
// 空输入（或全部被跳过）返回 nil。该函数从不读取文件，
// 也从不修改其输入。
func DescribeIgnoreRules(patterns []string) []IgnoreRuleInfo {
	var out []IgnoreRuleInfo
	for _, raw := range patterns {
		// 像文件加载器那样去掉前导 BOM；否则
		// 第一个"模式"会带有 BOM 前缀，而带 BOM 前缀的注释会
		// 被误认为真实规则。
		line := strings.TrimPrefix(strings.TrimSpace(raw), bom)
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") && strings.TrimSpace(line[1:]) == "" {
			// 裸 `!` 会被加载器丢弃，而不是成为
			// 匹配所有的重新包含；这里也照此处理。
			continue
		}
		info := describePattern(raw, line)
		if info.Glob == "" {
			continue
		}
		out = append(out, info)
	}
	return out
}

// describePattern 构建一个 IgnoreRuleInfo。line 是已去掉 BOM、
// 去掉空白的输入；raw 原样保留用于 Pattern 字段。
func describePattern(raw, line string) IgnoreRuleInfo {
	info := IgnoreRuleInfo{Pattern: raw}
	negated := strings.HasPrefix(line, "!")
	if negated {
		line = strings.TrimSpace(line[1:])
	}
	glob := normalizeIgnoreGlob(strings.Trim(line, "/"))
	info.Glob = glob
	info.Negated = negated

	action := "exclude"
	if negated {
		action = "re-include"
	}
	syntax := describeGlobSyntax(glob)
	if !strings.ContainsAny(glob, "*?[") {
		info.Description = fmt.Sprintf("%s the literal path %q (%s)", action, glob, syntax)
	} else {
		info.Description = fmt.Sprintf("%s paths matching glob %q [%s]", action, glob, syntax)
	}

	// 只有排除才会通过把路径排除在上下文之外而"伤害"它。
	// 用 Ignored 所使用的同一匹配器与探测路径匹配，
	// 因此被标记的模式保证确实匹配该路径。
	if !negated {
		var matched []string
		for _, probe := range sensitiveProbes {
			if security.GlobMatch(glob, probe) {
				matched = append(matched, probe)
			}
		}
		info.SensitiveRisk = len(matched) > 0
		info.RiskReason = strings.Join(matched, ", ")
	}
	return info
}

// describeGlobSyntax 说明某模式使用了哪些 glob 元字符。
func describeGlobSyntax(glob string) string {
	switch {
	case strings.Contains(glob, "**"):
		return "`**` spans directories"
	case strings.Contains(glob, "*"):
		return "`*` matches within one segment"
	case strings.Contains(glob, "?"):
		return "`?` matches one character"
	default:
		return "matches it or anything below it"
	}
}
