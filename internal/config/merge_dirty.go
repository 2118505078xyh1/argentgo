package config

import "strings"

// mergeDirtyFields 把 changed 中由 dirtyKeys 指定的字段合并进 base 的深拷贝，
// 返回一个全新的 map：未被标记为脏的字段保留 base 的值，脏字段从 changed
// 读取并覆盖到相同位置。
//
// dirtyKeys 的元素是点分路径，支持嵌套键（例如 "providers.x.baseURL"）。每一级
// 按字面量分段匹配，因此含 CJK 字符的键无需特殊处理。以下路径会被跳过，从而
// 保留 base 中的原值：
//   - 空路径；
//   - 在 changed 中不存在的路径（注意：显式 nil 值算作存在，会被写入）；
//   - 任一级不是对象、无法继续向下导航的路径。
//
// 同一路径前缀的多个脏键按给定顺序应用，后出现的覆盖先出现的；调用方应传入
// 自洽的 changed（例如由单一写入方基于同一份配置构造）。
//
// 本函数不修改 base 或 changed：返回值是 base 的深拷贝，写入的叶子值也来自
// changed 的深拷贝，因此调用方随后修改返回值不会反向影响任何入参。它可在
// 读-改-写场景中配合“重新读取磁盘最新内容作为 base”使用，只回写脏字段，
// 避免覆盖其它进程对非脏字段的并发修改。
func mergeDirtyFields(base, changed map[string]any, dirtyKeys []string) map[string]any {
	merged := cloneTree(base)
	for _, key := range dirtyKeys {
		if key == "" {
			continue
		}
		value, ok := lookupDotted(changed, key)
		if !ok {
			continue
		}
		setDottedValue(merged, key, cloneValue(value))
	}
	return merged
}

// lookupDotted 按点分路径在 tree 中查找值。任一级不是对象或不存在时返回 false。
func lookupDotted(tree map[string]any, key string) (any, bool) {
	segments := strings.Split(key, ".")
	var current any = tree
	for _, segment := range segments {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// setDottedValue 在点分路径处写入 value，并按需创建中间对象。
func setDottedValue(root map[string]any, key string, value any) {
	segments := strings.Split(key, ".")
	current := root
	for _, segment := range segments[:len(segments)-1] {
		next, ok := current[segment].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[segment] = next
		}
		current = next
	}
	current[segments[len(segments)-1]] = value
}
