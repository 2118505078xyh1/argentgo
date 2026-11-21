package app

import (
	"github.com/2118505078xyh1/argentgo/internal/config"
)

// 本文件把 config.ParseOptInDisplay 的展示投影映射成 app 层的 opt-in 开关映射，
// 让「配置文件」成为与 ARGENT_OPT_INS 并列的第二条激活入口。它与 ParseOptInEnv
// 一样是纯函数：只读入参、不读进程环境、不修改任何传入的映射，默认全关（fail-closed）。
// 把结果并入 Execute 装配面的接线由 app_execute.go 完成，本文件只提供原语。

// configOptInKeyMap 是 app 清单与 config 白名单的交集：只有两边都受支持的键才会被
// 配置激活。config 白名单中同时出现在 app OptInKey 清单内的键才列入本表；app 独有的
// 键（refusal_reason / untrusted_result / untrusted_boundary / compaction_fidelity）
// 不在其中，因此配置文件无法打开它们，只能通过 ARGENT_OPT_INS 环境变量激活。
// 若 config 未来新增 app 不认识的键，这里查表会落空并被忽略，保持 fail-closed。
var configOptInKeyMap = map[string]OptInKey{
	"cli_hints":                 OptInCliHints,
	"apply_patch":               OptInApplyPatch,
	"grep_ex":                   OptInGrepEx,
	"context_pressure":          OptInContextPressure,
	"attempt_isolation":         OptInAttemptIsolation,
	"keep_implicit_context":     OptInKeepImplicitContext,
	"preserve_summary_sections": OptInPreserveSummarySections,
	"tool_error_reflection":     OptInToolErrorReflection,
	"model_summary":             OptInModelSummary,
}

// configOptIns 从配置原始映射读取 opt-in 开关，产出只含「交集」键的开关映射。
// 它委托 config.ParseOptInDisplay 完成白名单过滤与 fail-closed 解析，因此：
//   - raw 为 nil、缺 optins 块、块类型不符、键非布尔值时，对应开关均为 false；
//   - 未知键（含 config 未来新增但 app 不认识的键）被忽略；
//   - 返回的映射为每个交集键显式给值，无配置时是 8 键全 false 的映射。
//
// 它是纯函数：不修改 raw 及其嵌套值，不读取全局状态，结果只由入参决定。
func configOptIns(raw map[string]any) map[OptInKey]bool {
	items := config.ParseOptInDisplay(raw)
	out := make(map[OptInKey]bool, len(items))
	for _, item := range items {
		if key, ok := configOptInKeyMap[item.Key]; ok {
			out[key] = item.Enabled
		}
	}
	return out
}

// mergeOptIns 合并环境激活面与配置激活面：任一来源显式开启即为开启（逻辑或）。
// 返回的映射是新建副本，只含两个入参出现过的键；调用方修改结果不会影响入参。
// 两入参都为空时返回非 nil 的空映射。
func mergeOptIns(envOpt, cfgOpt map[OptInKey]bool) map[OptInKey]bool {
	out := make(map[OptInKey]bool, len(envOpt)+len(cfgOpt))
	for key, enabled := range envOpt {
		out[key] = enabled
	}
	for key, enabled := range cfgOpt {
		out[key] = out[key] || enabled
	}
	return out
}
