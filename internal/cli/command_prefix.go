package cli

import (
	"sort"
	"strings"
	"unicode"
)

// foldCommandPrefix 把命令名按 Unicode 码点折叠为小写形式，用于大小写不敏感的
// 精确/前缀比较。全程按 rune 迭代并写回，不切分多字节的 CJK 字符（CJK 安全），
// 也不依赖 strings.ToLower 的特殊大小写规则，保证不同调用之间结果一致。
func foldCommandPrefix(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// resolveCommandPrefix 把用户输入的命令名解析为 commands 中的一个真实命令。
// 它是纯函数：只读参数，不产生 I/O、不访问配置/会话/全局状态，结果仅由入参决定，
// 因此既能安全地用于表驱动测试，也能在将来由命令分发路径 opt-in 调用，而不改变
// 当前任何命令的字节级输出（当前尚未接线，见接线点报告）。
//
// 解析规则（按优先级）：
//  1. 字节级精确命中：input 与某命令逐字节相同时，直接返回该命令；
//  2. 大小写不敏感的精确命中：按 rune 折叠后相等时返回对应的真实命令，避免
//     "audit" 同时被当作 "audit-log" 的前缀而误报歧义；
//  3. 前缀匹配：折叠后以 input 开头的命令构成候选集——
//     - 唯一命中：返回该命令，candidates 为 nil；
//     - 多命中：match 为空，candidates 为按字典序排序的真实命令列表；
//  4. 无命中（含空输入、空命令集）：match 为空，candidates 为 nil。
//
// 返回值中的命令均为 commands 里出现的原始字符串，而非折叠后的形式；候选列表
// 会去重，且顺序稳定（先按输入出现顺序收集，再按字典序排序）。
func resolveCommandPrefix(input string, commands []string) (match string, candidates []string) {
	if input == "" || len(commands) == 0 {
		return "", nil
	}

	// 阶段 1：字节级精确命中，保留调用方写入的原样。
	for _, cmd := range commands {
		if cmd == input {
			return cmd, nil
		}
	}

	foldedInput := foldCommandPrefix(input)

	// 阶段 2：大小写不敏感的精确命中。
	for _, cmd := range commands {
		if foldCommandPrefix(cmd) == foldedInput {
			return cmd, nil
		}
	}

	// 阶段 3：前缀匹配，收集并去重候选。
	seen := make(map[string]struct{})
	var hits []string
	for _, cmd := range commands {
		if !strings.HasPrefix(foldCommandPrefix(cmd), foldedInput) {
			continue
		}
		if _, ok := seen[cmd]; ok {
			continue
		}
		seen[cmd] = struct{}{}
		hits = append(hits, cmd)
	}

	switch len(hits) {
	case 0:
		return "", nil
	case 1:
		return hits[0], nil
	default:
		sort.Strings(hits)
		return "", hits
	}
}
