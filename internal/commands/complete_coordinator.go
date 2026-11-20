package commands

// 补全协调器：在扁平的可用字符串池之上，
// 按 aider 风格进行三级回退。
//
// CompleteCommands 和 CompleteArgument 被刻意设计成单一用途的纯
// 匹配器（一边是命令名，另一边是枚举的参数值）。
// Complete 用 aider 在其输入组件中采用的回退顺序将它们串联起来，
// 这样前端只需一次调用，不必在每种
// 语言绑定中重新实现规范化以及"当前适用哪个列表"的判断。
// 这里不读取文件、不启动进程，也不修改
// 调用方的切片。

import (
	"sort"
	"strings"
)

// Complete 返回从 available 中抽取的、针对 input 的补全候选。
//
// 依次尝试三级；第一个产生匹配的级别
// 胜出，因此歧义前缀会返回整组匹配项（即
// 下拉框需要渲染的"歧义列表"）。各级别如下：
//
//  1. 命令名 —— 当 input 以 '/' 开头时，将可用字符串
//     视为斜杠命令名，并用 CompleteCommands 匹配。
//     这复用其前导斜杠处理、大小写/宽度折叠、CJK 安全的 rune
//     匹配和别名规则；与 CompleteCommands 一样，部分键入的
//     参数（"/rev --all"）不会抑制名称匹配。
//  2. 命令专属候选 —— 末尾标记（或命令之后的标记，如 "/deploy st"）
//     命令，如 "/deploy st"）通过 available 匹配，使用
//     CompleteArgument 的枚举值规则。完整斜杠命令后的
//     尾随空格（"/deploy "）产生空前缀，即匹配所有候选。
//     candidate.
//  3. 标识符 —— CompleteArgument 无法表示的取值（含空白或 '|' 的
//     取值，或单个候选）按普通的、CJK 安全的
//     前缀匹配。这是 aider 对文件名和聊天标识符的回退方案。
//
// 匹配不区分大小写且会折叠宽度（全角 ASCII 与表意空格的表现
// 与半角形式一致），因此中文输入法在每个级别都
// 可用。结果经过去重，并按折叠后的小写值稳定排序，
// 原字符串作为并列时的决胜项；nil 或空输入
// 会列出 available 中的全部内容。返回的切片是全新的，
// 绝不与 available 共享底层数组。
func Complete(input string, available []string) []string {
	folded := foldWidth(input)
	trimmed := strings.TrimSpace(folded)
	trailingSpace := strings.TrimRight(folded, " \t\r\n") != folded

	if trimmed == "" {
		return sortedUnique(available)
	}

	current := lastToken(trimmed)
	if trailingSpace {
		current = ""
	}

	if strings.HasPrefix(trimmed, "/") {
		// 级别 1：CompleteCommands 忽略第一个标记之后的任何内容，因此
		// 部分键入的参数（"/rev --all"）仍能补全名称。
		if matched := completeCommandNames(available, trimmed); len(matched) > 0 {
			return matched
		}
	}

	if matched := completeArgumentNames(current, available); len(matched) > 0 {
		return matched // Level 2: enumerated command-specific candidates.
	}
	return completeIdentifiers(current, available) // Level 3: identifiers.
}

// completeCommandNames 将扁平的候选池适配给 CompleteCommands：
// 把每个字符串包装成命令名，再将匹配结果映射回名称。
func completeCommandNames(available []string, prefix string) []string {
	cmds := make([]Command, 0, len(available))
	for _, value := range available {
		if value != "" {
			cmds = append(cmds, Command{Name: value})
		}
	}
	matched := CompleteCommands(cmds, prefix)
	out := make([]string, 0, len(matched))
	for _, cmd := range matched {
		out = append(out, cmd.Name)
	}
	return sortedUnique(out)
}

// completeArgumentNames 将扁平池适配给 CompleteArgument：
// 把它编码为一个枚举的 argument-hint 组。含空白或 '|' 的
// 值无法通过提示标记往返传递，因此这类值留给
// 标识符级别处理。一个组至少需要两个不同值才算枚举；
// 单个候选同样交由标识符级别处理。
func completeArgumentNames(prefix string, available []string) []string {
	simple := make([]string, 0, len(available))
	for _, value := range available {
		if value == "" || strings.ContainsAny(value, " \t\r\n|") {
			continue
		}
		simple = append(simple, value)
	}
	if len(simple) < 2 {
		return nil
	}
	hint := "[" + strings.Join(simple, "|") + "]"
	return sortedUnique(CompleteArgument(Command{ArgumentHint: hint}, prefix))
}

// completeIdentifiers 是第 3 级：使用与参数补全相同的折叠、
// 不区分大小写的键做普通前缀匹配。它保留 '/' 和内部
// 空格，因此路径和多词标识符能得到合理的补全。
func completeIdentifiers(prefix string, available []string) []string {
	key := argumentKey(prefix)
	out := make([]string, 0, len(available))
	for _, value := range available {
		if value != "" && strings.HasPrefix(argumentKey(value), key) {
			out = append(out, value)
		}
	}
	return sortedUnique(out)
}

// lastToken 返回 s 中最后一个空白 rune 之后的部分。
func lastToken(s string) string {
	if i := strings.LastIndexAny(s, " \t\r\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// sortedUnique 返回按折叠键排序的新切片（原字符串作并列
// 决胜项），并去除完全重复项。它从不修改输入。
func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := argumentKey(out[i]), argumentKey(out[j])
		if ki != kj {
			return ki < kj
		}
		return out[i] < out[j]
	})
	uniq := out[:0]
	for _, value := range out {
		if len(uniq) == 0 || uniq[len(uniq)-1] != value {
			uniq = append(uniq, value)
		}
	}
	return uniq
}
