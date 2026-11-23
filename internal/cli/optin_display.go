package cli

import (
	"sort"
	"strings"
)

// 本文件提供 opt-in 开关的诊断展示原语。它只依赖标准库，不 import internal/app，
// 因此 cli 包可以独立生成"当前启用了哪些 opt-in 特性"的稳定中文描述。
// 这些函数不接线到任何现有命令；接线由调用方完成，以保证现有命令输出逐字节不变。

// optInEnvVar 是 opt-in 开关列表所在的单个环境变量名，与 app 侧注册表一致。
const optInEnvVar = "ARGENT_OPT_INS"

// optInAllOffMessage 是 ARGENT_OPT_INS 集中注册表未显式启用任何键时的稳定文案。
// 注意：它只描述集中注册表（15 键），不代表独立门控的状态——独立门控多数已默认开启。
const optInAllOffMessage = "未显式启用任何键（独立门控多数默认开启，见下表）"

// knownOptInKeys 是 cli 侧已知 opt-in 键的集合，与 app 侧注册表保持一致。
// cli 不 import app，因此这里复制一份稳定的配置词汇表；新增键时两处需同步。
// 未知键一律忽略（fail-closed），非 ASCII 文本不会命中任何键，解析结果与地区无关。
var knownOptInKeys = map[string]struct{}{
	"refusal_reason":            {},
	"untrusted_result":          {},
	"context_pressure":          {},
	"untrusted_boundary":        {},
	"attempt_isolation":         {},
	"compaction_fidelity":       {},
	"keep_implicit_context":     {},
	"preserve_summary_sections": {},
	"apply_patch":               {},
	"grep_ex":                   {},
	"cli_hints":                 {},
	"tool_error_reflection":     {},
	"model_summary":             {},
	"auto_verify":               {},
	"codegraph":                 {},
}

// FormatOptIns 把已启用的 opt-in 开关映射格式化为稳定的中文展示。
//
// 它只列出值为 true 的键，并按 key 的 UTF-8 字节序排序（sort.Strings），
// 因此输入映射的遍历顺序与写入顺序都不影响输出，CJK 键也能稳定参与排序。
// 函数只用整串写入（strings.Builder/WriteString），不做按字节的截断或按宽度
// 的填充，因此任何 UTF-8 字符都不会在中间被切开。入参只读、不被修改；
// 当没有任何启用的键（空映射或全为 false）时返回 optInAllOffMessage。
func FormatOptIns(enabled map[string]bool) string {
	keys := make([]string, 0, len(enabled))
	for key, on := range enabled {
		if on {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return optInAllOffMessage
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("已启用的 opt-in 特性：\n")
	for _, key := range keys {
		b.WriteString("- ")
		b.WriteString(key)
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// OptInFromEnv 从注入的 env 函数解析 ARGENT_OPT_INS：值为逗号分隔的键名，
// 两侧空白被忽略，未知键与空片段被忽略（fail-closed）。它只读取一次 env，
// 不读取进程环境，也不修改任何入参；返回的映射只含被启用的键（值为 true）。
//
// 语义与 app 侧 ParseOptInEnv 的解析规则一致，但 cli 不 import app；
// 另外当 env 为 nil 时返回空的非 nil 映射（而不是 app 侧的全 false 映射）。
func OptInFromEnv(env func(string) string) map[string]bool {
	out := make(map[string]bool)
	if env == nil {
		return out
	}
	for _, field := range strings.Split(env(optInEnvVar), ",") {
		key := strings.TrimSpace(field)
		if _, known := knownOptInKeys[key]; known {
			out[key] = true
		}
	}
	return out
}
