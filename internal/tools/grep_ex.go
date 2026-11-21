package tools

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 本文件提供一个"深度检索"原语 GrepEx 及其摘要格式化函数。
// 它刻意与具体工具接线解耦：不访问 context、不依赖 workspace 根、
// 不注册到工具注册表，默认关闭，由调用方按需接入。
// 所有 API 都是纯函数式的：不修改调用方传入的参数，输出稳定可排序。

const (
	// grepExDefaultHeadLimit 是 HeadLimit <= 0 时采用的默认上限，
	// 与包内已有的 maxMatches 保持一致。
	grepExDefaultHeadLimit = maxMatches

	grepExModeContent          = "content"
	grepExModeFilesWithMatches = "files_with_matches"
	grepExModeCount            = "count"
)

// GrepExOptions 描述一次深度检索的参数。
// 所有字段按值传入，GrepEx 不会修改它们。
type GrepExOptions struct {
	Pattern       string // 检索模式；Literal 为真时按子串处理，否则按正则表达式
	Path          string // 检索根目录（也可为单个文件）；为空时视为 "."
	Context       int    // 同时设置匹配行前后各取多少行上下文
	Before        int    // 前置上下文行数；>0 时覆盖 Context
	After         int    // 后置上下文行数；>0 时覆盖 Context
	OutputMode    string // 输出模式：content / files_with_matches / count，仅由 GrepExSummary 消费
	Include       string // include glob（以 '/' 分隔），为空表示不过滤
	CaseSensitive bool   // 是否大小写敏感
	Literal       bool   // 为真时用 strings.Contains，否则用 regexp
	HeadLimit     int    // 匹配总数上限；<=0 时取 grepExDefaultHeadLimit
}

// GrepExMatch 表示一处匹配及其上下文。
// Before / After 是独立拷贝的切片，调用方可自由修改而不影响其他结果。
type GrepExMatch struct {
	Path   string
	Line   int
	Text   string
	Before []string
	After  []string
}

// GrepEx 按 opts 在 Path 下逐行检索文件，返回匹配列表与每个文件的匹配计数。
//
// 语义要点：
//   - 遍历复用包内的 walkFiles，自动跳过 .git / node_modules 等嘈杂目录；
//   - Include 非空时用包内 globMatch 对相对路径与文件名做过滤；
//   - Literal 用 strings.Contains（不敏感时先 ToLower），否则用 regexp
//     （不敏感时在表达式前加 "(?i)"）；
//   - Context 同时决定前后行数，Before / After 为正数时分别覆盖；负值按 0 处理；
//   - HeadLimit 钳制匹配总数，<=0 时取默认值 200；
//   - 跳过含 NUL 的二进制文件；按 rune 处理，CJK 安全；
//   - 结果先按路径再按行号稳定排序；files 的计数与 matches 保持一致。
//
// 返回的 Path 是相对 opts.Path 的、以 '/' 分隔的路径；当 opts.Path 指向
// 单个文件时，Path 为该文件的名字。
func GrepEx(opts GrepExOptions) (matches []GrepExMatch, files map[string]int, err error) {
	files = make(map[string]int)

	base := opts.Path
	if strings.TrimSpace(base) == "" {
		base = "."
	}

	before, after := opts.Context, opts.Context
	if opts.Before > 0 {
		before = opts.Before
	}
	if opts.After > 0 {
		after = opts.After
	}
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}

	limit := opts.HeadLimit
	if limit <= 0 {
		limit = grepExDefaultHeadLimit
	}

	// 预编译匹配器；literal 模式预先算好小写 needle，避免在每行重复计算。
	var re *regexp.Regexp
	var needle string
	if opts.Literal {
		needle = opts.Pattern
		if !opts.CaseSensitive {
			needle = strings.ToLower(needle)
		}
	} else {
		src := opts.Pattern
		if !opts.CaseSensitive {
			src = "(?i)" + src
		}
		re, err = regexp.Compile(src)
		if err != nil {
			return nil, nil, fmt.Errorf("grep_ex: 非法模式 %q: %w", opts.Pattern, err)
		}
	}
	lineMatches := func(line string) bool {
		if opts.Literal {
			if opts.CaseSensitive {
				return strings.Contains(line, opts.Pattern)
			}
			return strings.Contains(strings.ToLower(line), needle)
		}
		return re.MatchString(line)
	}

	walkFiles(base, func(file string) bool {
		data, readErr := os.ReadFile(file)
		if readErr != nil || strings.ContainsRune(string(data), 0) {
			return true
		}

		rel, relErr := filepath.Rel(base, file)
		if relErr != nil || rel == "." {
			rel = filepath.Base(file)
		}
		rel = filepath.ToSlash(rel)

		if opts.Include != "" &&
			!globMatch(opts.Include, rel) && !globMatch(opts.Include, path.Base(rel)) {
			return true
		}

		lines := strings.Split(string(data), "\n")
		if strings.HasSuffix(string(data), "\n") && len(lines) > 0 {
			lines = lines[:len(lines)-1] // 去掉末尾换行产生的空行
		}
		for i := range lines {
			lines[i] = strings.TrimSuffix(lines[i], "\r") // 归一 CRLF
		}

		for i, line := range lines {
			if !lineMatches(line) {
				continue
			}
			match := GrepExMatch{
				Path: rel,
				Line: i + 1,
				Text: line,
			}
			// 前置上下文：左边界钳制到 0。
			start := i - before
			if start < 0 {
				start = 0
			}
			if start < i {
				match.Before = append([]string(nil), lines[start:i]...)
			}
			// 后置上下文：右边界钳制到 len(lines)-1。
			end := i + after
			if end >= len(lines) {
				end = len(lines) - 1
			}
			if end > i {
				match.After = append([]string(nil), lines[i+1:end+1]...)
			}

			matches = append(matches, match)
			files[rel]++
			if len(matches) >= limit {
				return false
			}
		}
		return true
	})

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	return matches, files, nil
}

// GrepExSummary 把 GrepEx 的结果格式化为稳定的中文摘要。
// mode 取值见 GrepExOptions.OutputMode；未知模式按 content 处理。
// 遍历 map 前先对键排序，保证输出稳定。
func GrepExSummary(matches []GrepExMatch, files map[string]int, mode string) string {
	if len(matches) == 0 {
		return "未找到匹配。"
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	switch mode {
	case grepExModeFilesWithMatches:
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 个文件包含匹配：\n", len(paths))
		for _, p := range paths {
			b.WriteString(p)
			b.WriteByte('\n')
		}
		return strings.TrimSuffix(b.String(), "\n")

	case grepExModeCount:
		var b strings.Builder
		fmt.Fprintf(&b, "共 %d 处匹配，分布在 %d 个文件：\n", len(matches), len(paths))
		for _, p := range paths {
			fmt.Fprintf(&b, "%s: %d\n", p, files[p])
		}
		return strings.TrimSuffix(b.String(), "\n")

	default: // content
		var b strings.Builder
		fmt.Fprintf(&b, "找到 %d 处匹配，分布在 %d 个文件：\n", len(matches), len(paths))
		for _, m := range matches {
			for _, before := range m.Before {
				fmt.Fprintf(&b, "  < %s\n", before)
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", m.Path, m.Line, m.Text)
			for _, after := range m.After {
				fmt.Fprintf(&b, "  > %s\n", after)
			}
		}
		return strings.TrimSuffix(b.String(), "\n")
	}
}
