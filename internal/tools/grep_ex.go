package tools

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// 本文件提供一个"深度检索"原语 GrepEx 及其摘要格式化函数。
// 检索走共享的流式扫描器与并行目录遍历：小文件单次读、大文件分块、
// 超长行跳过、二进制前置检测，返回结构化截断信号供工具层生成提示。

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
	MaxPerFile    int    // 单个文件的匹配上限；>0 时某文件达到该数即停止扫描该文件，但继续遍历其它文件

	// RelRoot 非空时作为相对路径的基准（grep 工具传工作区根）；
	// 为空时回退到 Path，保持既有调用方的路径语义不变。
	RelRoot string
	// Ctx 非空时用于并行遍历的取消；为空时用 context.Background()。
	Ctx context.Context
	// TrimText 为真时对文本做 strings.TrimSpace（普通 grep 的历史行为）。
	TrimText bool
	// MaxColumns 为单行输出列（rune）上限；<=0 时用 searchScanMaxColumns。
	MaxColumns int
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

// GrepEx 是 GrepExWithOutcome 的兼容包装：丢弃截断信号，返回三元组。
//
// 为保持历史调用方的契约，这里仍对明显非法的正则直接报错；工具层
// （grep / grep_ex）走 GrepExWithOutcome，享受消毒 + 括号转义 + 字面量
// 兜底的容错编译链。这样既有 golden 与容错能力可以同时成立。
func GrepEx(opts GrepExOptions) (matches []GrepExMatch, files map[string]int, err error) {
	if !opts.Literal {
		src := opts.Pattern
		if !opts.CaseSensitive {
			src = "(?i)" + src
		}
		if _, compileErr := regexp.Compile(src); compileErr != nil {
			return nil, nil, fmt.Errorf("grep_ex: 非法模式 %q: %w", opts.Pattern, compileErr)
		}
	}
	matches, files, _, err = GrepExWithOutcome(opts)
	return matches, files, err
}

// GrepExWithOutcome 按 opts 在 Path 下逐行检索文件，返回匹配列表、
// 每个文件的匹配计数，以及结构化截断信号。
//
// 语义要点：
//   - 遍历走并行 walkFiles，自动跳过 .git / node_modules 等嘈杂目录；
//   - Include 非空时用包内 globMatch 对相对路径与文件名做过滤；
//   - Literal 用字节包含（不敏感时先 ToLower），否则用容错正则编译链；
//   - Context 同时决定前后行数，Before / After 为正数时分别覆盖；负值按 0 处理；
//   - HeadLimit 钳制匹配总数，<=0 时取默认值 200；采用 limit+1 探测，
//     多取的一条仅用于判定 truncated，不进入返回结果；
//   - 小文件单次读入、大文件流式扫描；行超 64 MiB 跳过；列按 rune 截断；
//   - 结果先按路径再按行号稳定排序；files 的计数与 matches 保持一致。
func GrepExWithOutcome(opts GrepExOptions) (matches []GrepExMatch, files map[string]int, outcome searchOutcome, err error) {
	files = make(map[string]int)

	base := opts.Path
	if strings.TrimSpace(base) == "" {
		base = "."
	}
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	relRoot := opts.RelRoot
	if relRoot == "" {
		relRoot = base
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
	probe := probeLimit(limit)
	columns := opts.MaxColumns
	if columns <= 0 {
		columns = searchScanMaxColumns
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
		// compileSearchPattern 自己处理 (?i) 前缀与各种降级；
		// 保留原语义：不敏感时在用户模式前加 (?i)。
		var level regexFallback
		var hint string
		re, _, level, hint = compileSearchPattern(src, !opts.CaseSensitive)
		if level != regexExact {
			outcome.RegexHint = strings.TrimSuffix(hint, "。")
		}
	}
	lineMatches := func(line []byte) bool {
		if opts.Literal {
			if opts.CaseSensitive {
				return bytes.Contains(line, []byte(opts.Pattern))
			}
			// 不敏感模式必须同时小写被检索行；只小写 needle 会漏掉
			// 原文里的大写（如 "Hello world" 对 needle "hello"）。
			return strings.Contains(strings.ToLower(string(line)), needle)
		}
		return re.Match(line)
	}
	toText := func(line []byte) string {
		text := capLineRunes(string(line), columns)
		if opts.TrimText {
			text = strings.TrimSpace(text)
		}
		return text
	}

	var mu sync.Mutex
	var stopped atomic.Bool
	count := 0

	walkFiles(ctx, base, func(file string) bool {
		rel, relErr := filepath.Rel(relRoot, file)
		if relErr != nil || rel == "." {
			rel = filepath.Base(file)
		}
		rel = filepath.ToSlash(rel)

		if opts.Include != "" &&
			!globMatch(opts.Include, rel) && !globMatch(opts.Include, path.Base(rel)) {
			return true
		}

		lastIdx := -1
		fileCount := 0
		cb := newContextBuffer(before, after, func(kind byte, no int, text string) {
			mu.Lock()
			defer mu.Unlock()
			if stopped.Load() {
				return
			}
			switch kind {
			case contextMatch:
				if count >= probe {
					stopped.Store(true)
					return
				}
				count++
				matches = append(matches, GrepExMatch{Path: rel, Line: no, Text: text})
				lastIdx = len(matches) - 1
				fileCount++
			case contextBefore:
				if lastIdx >= 0 {
					matches[lastIdx].Before = append(matches[lastIdx].Before, text)
				}
			case contextAfter:
				if lastIdx >= 0 {
					matches[lastIdx].After = append(matches[lastIdx].After, text)
				}
			}
		})

		scanSearchFileLines(file, func(line scanLine) bool {
			matched := lineMatches(line.text)
			cb.push(line.no, line.text, matched, toText)
			if stopped.Load() || (opts.MaxPerFile > 0 && fileCount >= opts.MaxPerFile) || count >= probe {
				return false
			}
			return true
		})
		return !stopped.Load()
	})

	outcome.Limit = limit
	if ctx.Err() != nil {
		outcome.TimedOut = true
	}
	if count > limit {
		outcome.Truncated = true
		matches = matches[:limit]
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		return matches[i].Line < matches[j].Line
	})
	files = make(map[string]int, len(matches))
	for _, m := range matches {
		files[m.Path]++
	}
	return matches, files, outcome, nil
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
