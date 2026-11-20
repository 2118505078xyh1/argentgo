package commands

// 命令参数补全。
//
// CompleteArgument 是针对单个斜杠命令参数的纯匹配器：它
// 从不读取文件、启动进程或修改其参数。候选
// 值来自命令的 frontmatter `argument-hint`。某个提示
// 标记（去掉一层可选的 <...> 或 [...] 包裹后）
// 若包含 '|'，则视为枚举选项，如 `dev|stage|prod` 或
// `[dev|stage|prod]`；每个备选项都是一个候选。像
// `<env>` 或 `[file]` 这样的普通占位符描述开放取值，不产生候选。

import "strings"

// CompleteArgument 返回 cmd 的 argument-hint 中以 argPrefix
// 开头的候选值，按提示中的顺序排列。匹配不区分大小写且
// 按 rune 安全处理（全角 ASCII 折叠为半角，因此 CJK 输入法可用），但
// 返回的字符串是未经改动的候选，可直接插入。
//
// Rules:
//
//   - 空的 argPrefix 匹配所有候选，因此一个函数同时服务于
//     空查询列举和增量补全。
//   - 以空白分隔的提示标记各自独立检查；含 '|' 的标记
//     贡献其备选项，其余被忽略。
//   - 拆分前去掉一层可选的 <...> 或 [...] 包裹，因此
//     `[a|b]` 和 `a|b` 都能被识别。
//   - 空备选项和重复项被丢弃；少于两个不同值的组
//     不算枚举，予以忽略。
//   - 没有提示，或提示只含开放占位符的命令没有候选，
//     返回 nil；候选非空但无一匹配 argPrefix 时
//     同样返回 nil。
func CompleteArgument(cmd Command, argPrefix string) []string {
	candidates := argumentCandidates(cmd.ArgumentHint)
	if len(candidates) == 0 {
		return nil
	}
	key := argumentKey(argPrefix)
	var out []string
	for _, candidate := range candidates {
		if key == "" || strings.HasPrefix(argumentKey(candidate), key) {
			out = append(out, candidate)
		}
	}
	return out
}

// argumentKey 规范化候选或用户键入的前缀以便比较。
// 与 completionKey 不同，它保留 '/' 和内部空格：参数可能是
// 路径或多词取值。
func argumentKey(s string) string {
	return strings.ToLower(strings.TrimSpace(foldWidth(s)))
}

// argumentCandidates 从 argument-hint 中提取枚举值。
func argumentCandidates(hint string) []string {
	var out []string
	for _, token := range strings.Fields(hint) {
		bare := token
		if len(bare) >= 2 {
			first, last := bare[0], bare[len(bare)-1]
			if (first == '<' && last == '>') || (first == '[' && last == ']') {
				bare = bare[1 : len(bare)-1]
			}
		}
		if !strings.Contains(bare, "|") {
			continue // a plain placeholder, not a choice
		}
		parts := strings.Split(bare, "|")
		group := make([]string, 0, len(parts))
		seen := make(map[string]bool, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			group = append(group, part)
		}
		if len(group) >= 2 {
			out = append(out, group...)
		}
	}
	return out
}
