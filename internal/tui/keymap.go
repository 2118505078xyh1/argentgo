package tui

import (
	"sort"
	"strings"
)

// 本文件提供与现有按键解码/分派正交的"多别名"查询能力。
// 现有分发基于解码后的 Key.Kind 枚举（见 model_input.go 的 handleKeyLocked），
// 这里则以声明式的"动作 -> 别名列表"表为输入，按按键串反查动作名。
// 纯函数不触碰全局状态和终端字节流，因此可安全用于配置预览、帮助提示
// 以及未来可选接入的键位映射层。

// resolveBinding 在别名表中把按键串解析为动作名。
//
// aliases 把动作名映射到该动作的别名列表，例如：
//
//	"down" -> []string{"down", "ctrl+j", "j"}
//	"copy" -> []string{"c", "y"}
//
// 给定按键串 key，函数返回第一个把 key 列为别名的动作。
// 匹配大小写不敏感（基于 Unicode 大小写折叠，因此 "J" 与 "j" 等价；
// CJK 等无大小写的字符按原样逐字比较），支持形如 "复制" 的中文别名。
// 当多个动作声明了同一个别名（歧义）时，按动作名的字典序取第一个，
// 使结果与 map 的随机遍历顺序无关、稳定可复现。
// 空按键、空表或未命中均返回 ("", false)。
func resolveBinding(key string, aliases map[string][]string) (action string, ok bool) {
	if key == "" || len(aliases) == 0 {
		return "", false
	}

	// 对动作名排序，消除 map 遍历顺序带来的不确定性。
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, alias := range aliases[name] {
			if strings.EqualFold(key, alias) {
				return name, true
			}
		}
	}
	return "", false
}

// addAlias 把别名 alias 追加到动作 action 的别名列表。
// 仅当该别名（大小写不敏感地）尚未出现时才追加，避免重复；
// 空别名或 nil 表不会写入。首次为某动作添加别名时自动创建切片。
// 返回是否实际写入了新别名。
func addAlias(aliases map[string][]string, action, alias string) bool {
	if aliases == nil || alias == "" {
		return false
	}
	for _, existing := range aliases[action] {
		if strings.EqualFold(existing, alias) {
			return false
		}
	}
	aliases[action] = append(aliases[action], alias)
	return true
}

// mergeAliases 把 src 中的别名合并进 dst（就地修改 dst）。
// 按动作名排序后依次经 addAlias 添加，因此合并结果与 map 遍历顺序
// 无关，且大小写不敏感的重复别名会被跳过。src 为空时不修改 dst。
// 调用方须保证 dst 非 nil（nil 表无法就地写入）。
func mergeAliases(dst, src map[string][]string) {
	if len(src) == 0 || dst == nil {
		return
	}
	names := make([]string, 0, len(src))
	for name := range src {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, alias := range src[name] {
			addAlias(dst, name, alias)
		}
	}
}
