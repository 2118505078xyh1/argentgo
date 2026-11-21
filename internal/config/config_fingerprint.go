package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
)

// FingerprintConfig 返回配置原始字节的稳定内容指纹：SHA-256 摘要的
// 小写十六进制形式，固定 64 个字符。
//
// 它只读取 data，不保留对它的引用，也不关心字节是否为合法 JSON —— 同样的
// 字节序列总是得到同样的字符串，任意两个不同字节序列在密码学意义上不会
// 碰撞。空输入返回空串的 SHA-256（e3b0c442…），而不是空字符串，这样
// "无内容" 与 "真实指纹" 不会混淆。
//
// 该函数默认没有调用点，仅作为快照校验等场景按需使用的基础原语。
func FingerprintConfig(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ConfigDiff 描述两份配置映射之间的结构化差异。
//
// AddedKeys / RemovedKeys / ModifiedKeys 分别是新增、删除、值被修改的叶子
// key 路径（点分隔）；每个列表都按字符串升序稳定排序。Changed 为三者是否
// 至少有一个非空。
type ConfigDiff struct {
	Changed      bool
	AddedKeys    []string
	RemovedKeys  []string
	ModifiedKeys []string
}

// DiffConfigMaps 比较两份配置映射，产出稳定排序的 key 路径差异。
//
// 比较前先把映射递归展平：继续深入嵌套的 map[string]any，路径以 "." 连接；
// 其余值（标量、切片等）作为叶子，用 fmt.Sprint 转成字符串后比较。因此
// "providers": {"openai": {"model": "x"}} 会被展平为
// "providers.openai.model"。
//
// 稳定性保证：
//   - 展平时每层先对 key 排序再遍历，避免 Go map 随机遍历顺序影响结果；
//   - 三个结果列表各自 sort.Strings，输出与传入映射的构造顺序无关。
//
// 它是纯函数：不修改 before / after，也不持有其中任何值。默认没有调用点。
func DiffConfigMaps(before, after map[string]any) ConfigDiff {
	flatBefore := flattenConfigMap(before)
	flatAfter := flattenConfigMap(after)

	var added, removed, modified []string
	for key, value := range flatAfter {
		old, exists := flatBefore[key]
		switch {
		case !exists:
			added = append(added, key)
		case old != value:
			modified = append(modified, key)
		}
	}
	for key := range flatBefore {
		if _, exists := flatAfter[key]; !exists {
			removed = append(removed, key)
		}
	}

	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(modified)

	return ConfigDiff{
		Changed:      len(added) > 0 || len(removed) > 0 || len(modified) > 0,
		AddedKeys:    added,
		RemovedKeys:  removed,
		ModifiedKeys: modified,
	}
}

// flattenConfigMap 把配置映射递归展平为 "点分隔路径 -> fmt.Sprint 值"。
//
// 每层先对 key 排序再递归，使存在 "a.b" 这样的字面 key 与嵌套 a.b 的
// 路径碰撞时，解析结果仍是确定的（排序后最后一次写入胜出）。
func flattenConfigMap(m map[string]any) map[string]string {
	flat := make(map[string]string)

	var walk func(prefix string, cur map[string]any)
	walk = func(prefix string, cur map[string]any) {
		keys := make([]string, 0, len(cur))
		for key := range cur {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if child, ok := cur[key].(map[string]any); ok {
				walk(path, child)
				continue
			}
			flat[path] = fmt.Sprint(cur[key])
		}
	}
	walk("", m)

	return flat
}
