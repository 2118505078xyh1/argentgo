package config

import (
	"encoding/json"
	"sort"
	"strings"
)

// changeDiffRedacted 是敏感键在差异列表中的固定占位值。任何敏感键的
// Old/New 都不会暴露配置原文，只用它表示"这里有一个被隐藏的值"。
const changeDiffRedacted = "<redacted>"

// changeDiffSecretNames 是归一化后精确匹配即视为敏感的配置段名。
// 只收录明确表示凭据的段名；像 "key"、"auth" 这类过于宽泛的词刻意
// 不收录，以免把普通配置项错误脱敏。
var changeDiffSecretNames = map[string]bool{
	"apikey":        true,
	"token":         true,
	"secret":        true,
	"password":      true,
	"passwd":        true,
	"authorization": true,
	"credential":    true,
	"credentials":   true,
	"accesskey":     true,
	"privatekey":    true,
}

// changeDiffSecretSuffixes 是归一化段名的敏感后缀：段名以它们结尾时同样
// 视为敏感，覆盖 serveToken / openaiApiKey / dbPassword 之类的复合命名。
// 刻意不含裸 "key"，避免 monkey、keyboard 之类误伤。
var changeDiffSecretSuffixes = []string{
	"apikey", "token", "secret", "password", "passwd", "credential", "credentials",
}

// ConfigChange 描述配置中一个键的变化。
//
// Key 是点分隔的叶子路径（嵌套 map 逐层展开，例如
// "providers.openai.apiKey"）；Old 是变更前的字符串值，New 是变更后的
// 字符串值。新增项 Old 为空，删除项 New 为空。若键被判定为敏感
// （见 changeDiffSensitive），非空的 Old/New 一律替换为 changeDiffRedacted，
// 且 Sensitive 为 true —— 调用方据此知道"值变了"，但拿不到原文。
type ConfigChange struct {
	Key       string
	Old, New  string
	Sensitive bool
}

// DiffConfig 比较两份 JSON（或 JSONC）配置原始字节，产出可读的变更列表。
//
// 行为约定（无 error 返回通道，故用返回值本身表达失败）：
//   - 任一侧无法解析、或顶层不是 JSON 对象时，返回 nil（空变更列表），
//     不 panic、不返回部分结果；
//   - 入参允许 JSONC 语法：先经 StripJSONC 去掉注释、尾随逗号与 BOM，
//     再按严格 JSON 解析；
//   - 比较前把两侧递归展平为 "点分隔路径 -> fmt.Sprint 值"（复用
//     flattenConfigMap），因此嵌套键、CJK 键、非 map 叶子都按同一规则处理；
//   - 结果按 Key 的 UTF-8 字节序稳定升序排序，相同输入总是得到相同输出；
//   - 敏感键的值会被脱敏，绝不把原文写进 Old/New；
//   - 它是纯函数：只读入参，不修改、不持有、不缓存任何输入字节或值。
//     无变化时返回 nil。
//
// 该函数默认没有调用点，仅作为配置热重载等场景按需使用的基础原语。
func DiffConfig(oldRaw, newRaw []byte) []ConfigChange {
	before, okBefore := parseChangeDiffConfig(oldRaw)
	after, okAfter := parseChangeDiffConfig(newRaw)
	if !okBefore || !okAfter {
		return nil
	}

	flatBefore := flattenConfigMap(before)
	flatAfter := flattenConfigMap(after)

	// 先用 map 去重，再排序，保证输出与 map 的随机遍历顺序无关。
	seen := make(map[string]bool, len(flatBefore)+len(flatAfter))
	changes := make([]ConfigChange, 0, len(flatBefore)+len(flatAfter))
	appendChange := func(key, oldValue, newValue string, inBefore, inAfter bool) {
		sensitive := changeDiffSensitive(key)
		if sensitive {
			// 只隐藏非空侧，保留"新增/删除/修改"的方向信息。
			if inBefore {
				oldValue = changeDiffRedacted
			}
			if inAfter {
				newValue = changeDiffRedacted
			}
		}
		changes = append(changes, ConfigChange{
			Key:       key,
			Old:       oldValue,
			New:       newValue,
			Sensitive: sensitive,
		})
	}

	for key, newValue := range flatAfter {
		oldValue, inBefore := flatBefore[key]
		seen[key] = true
		if inBefore && oldValue == newValue {
			continue
		}
		appendChange(key, oldValue, newValue, inBefore, true)
	}
	for key, oldValue := range flatBefore {
		if seen[key] {
			continue
		}
		seen[key] = true
		appendChange(key, oldValue, "", true, false)
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	if len(changes) == 0 {
		return nil
	}
	return changes
}

// parseChangeDiffConfig 把配置字节解析为顶层对象。
//
// 第二个返回值为 false 表示"不是可用配置"：空输入、JSONC 清理后仍非
// 严格 JSON、或顶层是标量/数组/null。调用方据此返回空差异。
func parseChangeDiffConfig(raw []byte) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	cleaned := StripJSONC(string(raw))
	var value any
	if err := json.Unmarshal([]byte(cleaned), &value); err != nil {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return obj, true
}

// changeDiffSensitive 判断点分隔路径中是否有任一段看起来是凭据键。
//
// 每段先归一化（转小写、去掉 '_' 和 '-'），再依次做精确名匹配和敏感
// 后缀匹配。按段而非整串匹配，因此 "providers.openai.apiKey" 能被识别；
// 对 CJK 段名归一化是空操作，自然不会命中英文敏感词。
func changeDiffSensitive(path string) bool {
	for _, segment := range strings.Split(path, ".") {
		normalized := normalizeChangeDiffSegment(segment)
		if normalized == "" {
			continue
		}
		if changeDiffSecretNames[normalized] {
			return true
		}
		for _, suffix := range changeDiffSecretSuffixes {
			if strings.HasSuffix(normalized, suffix) {
				return true
			}
		}
	}
	return false
}

// normalizeChangeDiffSegment 归一化单个键段，使 apiKey / api_key / API-KEY
// 收敛到同一形式。分隔符只处理 '_' 与 '-'，'.' 由调用方当作路径分隔符。
func normalizeChangeDiffSegment(segment string) string {
	segment = strings.ToLower(segment)
	segment = strings.ReplaceAll(segment, "_", "")
	return strings.ReplaceAll(segment, "-", "")
}
