package config

import (
	"sort"
	"strings"
)

// ModelFacingEnv 是面向模型的 shell 环境覆盖表：自研 shell 在启动供模型
// 使用的子进程时，应当把这些键强制为下表规定的值，以抑制花哨的终端输出、
// 关闭分页器，让模型拿到稳定、纯文本、无 ANSI 转义的输出。
//
// 该变量按“只读约定”使用：包内代码只读取、不修改它。调用方也不得修改，
// 任何修改都会影响后续所有 ApplyModelEnv 调用的结果。如需派生新值，请用
// extra 参数传入非受管键（受管键不可被 extra 覆盖，见 ReservedKeys）。
//
// 所有键都是 ASCII；含 CJK 字符的键不会命中本表。
var ModelFacingEnv = map[string]string{
	"NO_COLOR":  "1",
	"TERM":      "dumb",
	"PAGER":     "cat",
	"GIT_PAGER": "cat",
}

// ReservedKeys 返回“受管保留键”集合，即不允许被 extra 覆盖的键集合，
// 其成员恰好是 ModelFacingEnv 的键。
//
// 返回值每次都是新分配的 map，调用方可自由增删而不影响包内状态。
// 返回的 map 中值恒为 struct{}{}，只用作集合判断（键是否存在）。
func ReservedKeys() map[string]struct{} {
	keys := make(map[string]struct{}, len(ModelFacingEnv))
	for k := range ModelFacingEnv {
		keys[k] = struct{}{}
	}
	return keys
}

// splitEnvEntry 把一个 "KEY=VALUE" 条目拆成键与值。
// 缺少 '=' 或键为空的条目被视为非法，返回 ok=false 并由调用方原样保留。
// 只按第一个 '=' 切分，值中后续的 '=' 属于值本身。
func splitEnvEntry(entry string) (key, value string, ok bool) {
	idx := strings.IndexByte(entry, '=')
	if idx <= 0 {
		return "", "", false
	}
	return entry[:idx], entry[idx+1:], true
}

// sortedMapKeys 返回 map 的键按字节序排序后的切片，保证遍历 map 时结果
// 确定（map 遍历顺序是随机的）。供需要稳定输出顺序的逻辑使用。
func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ApplyModelEnv 生成一个面向模型使用的环境切片。语义如下：
//
//  1. 从左到右扫描 base（"KEY=VALUE" 形式）：若键是受管键（在
//     ModelFacingEnv 中），值被强制为规定值；其余键值原样保留。
//  2. 受管键无论 base 中是否出现，都保证出现在结果中且为规定值，
//     以便把覆盖项真正“注入”到环境里。
//  3. 应用 extra 中的额外覆盖：受管保留键（见 ReservedKeys）一律被
//     忽略，不得被 extra 覆盖；其余键若已存在则原地改写其值，
//     不存在则追加。
//
// 结果按“首次出现的位置”保序：重复的键只保留一条，出现在最前面的
// 位置，但其值由最后一次写入决定（后写覆盖先写，符合环境变量惯例）。
// 追加 base 中没有的键（受管键与 extra 新键）时按键名排序，保证确定性。
//
// 缺少 '=' 或键为空的 base 条目原样保留，不参与去重。
// 本函数是纯函数：不读写环境或磁盘，不修改 base 与 extra，
// 始终返回一个新切片；相同输入得到相同输出。
func ApplyModelEnv(base []string, extra map[string]string) []string {
	result := make([]string, 0, len(base)+len(ModelFacingEnv)+len(extra))
	index := make(map[string]int, len(base)+len(extra)) // 键 -> result 中的位置

	// set 写入一个键值：已存在则原地覆盖（保留首次位置），否则追加。
	set := func(key, value string) {
		if pos, exists := index[key]; exists {
			result[pos] = key + "=" + value
			return
		}
		index[key] = len(result)
		result = append(result, key+"="+value)
	}

	// 1. 处理 base。
	for _, entry := range base {
		key, value, ok := splitEnvEntry(entry)
		if !ok {
			result = append(result, entry)
			continue
		}
		if managed, exists := ModelFacingEnv[key]; exists {
			value = managed
		}
		set(key, value)
	}

	// 2. 受管键必定出现且为规定值（排序保证确定性）。
	for _, key := range sortedMapKeys(ModelFacingEnv) {
		set(key, ModelFacingEnv[key])
	}

	// 3. 应用 extra；受管保留键忽略，其余覆盖或追加。
	reserved := ReservedKeys()
	for _, key := range sortedMapKeys(extra) {
		if _, isReserved := reserved[key]; isReserved {
			continue
		}
		set(key, extra[key])
	}

	return result
}

// sensitiveKeyMarkers 用于识别敏感环境变量键名。带下划线的短词（TOKEN、
// KEY）要求带前导下划线，以避免把 MONKEY 之类误判为敏感键；
// PASSWORD/PASSWD/SECRET/CREDENTIAL 是具有辨识度的长词，可直接子串匹配。
var sensitiveKeyMarkers = [...]string{
	"_TOKEN", "_KEY",
	"PASSWORD", "PASSWD", "SECRET", "CREDENTIAL",
}

// isSensitiveEnvKey 判断单个环境变量键名是否敏感（大小写不敏感）。
// 空键与含 CJK 字符的键不会被判为敏感（CJK 无关）。
func isSensitiveEnvKey(key string) bool {
	if key == "" {
		return false
	}
	upper := strings.ToUpper(key)
	// 整键名恰好是敏感词。
	switch upper {
	case "TOKEN", "KEY", "PASSWORD", "PASSWD", "SECRET", "CREDENTIAL":
		return true
	}
	for _, marker := range sensitiveKeyMarkers {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

// StripCredentials 从环境切片中移除键名敏感（“*_TOKEN”、“*_KEY”、
// “PASSWORD”等，大小写不敏感）的条目，例如 OPENAI_API_KEY、ghp_TOKEN、
// DB_PASSWORD、my_secret 等，防止凭据被写入日志或传给模型。
//
// 键的识别按第一个 '=' 切分；缺少 '=' 或键为空的条目以整条字符串作为
// 键名判断。匹配的判断对大小写不敏感，含 CJK 字符的键名不受影响。
// 本函数是纯函数：不修改入参，保留其余条目的原始内容与相对顺序，
// 并始终返回一个新切片。
func StripCredentials(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, ok := splitEnvEntry(entry)
		if !ok {
			key = entry
		}
		if isSensitiveEnvKey(key) {
			continue
		}
		result = append(result, entry)
	}
	return result
}
