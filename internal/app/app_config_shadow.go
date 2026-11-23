package app

import (
	"fmt"
	"strings"
)

// 更高优先级来源的规范化类别。它们会遮蔽配置文件中的同名键：
// 配置文件写入成功，但运行值仍由 env/flag 决定。
const (
	shadowKindEnv  = "env"
	shadowKindFlag = "flag"
)

// isKeyShadowed 判断 key 是否被更高优先级来源（env/flag）遮蔽，并返回遮蔽来源。
//
// sources 是"为该键提供值的来源"列表，按优先级从低到高排列，与
// config.Config.Sources 的顺序一致。列表里若存在 env 或 flag 层，写配置文件
// 就不会改变运行值——运行值由这些更高优先级的层决定，从而形成
// "set 成功但运行值不变"的静默失败。
//
// 返回规则：无遮蔽时返回 (false, "")；有遮蔽时返回 (true, 遮蔽来源原文)。
// 当同时存在多个遮蔽来源时，取优先级最高的一个（flag 高于 env；同级取列表中
// 靠后的一个），因此结果稳定、可复现，不依赖遍历顺序以外的偶然因素。
//
// 该函数是纯函数：不读环境、不写状态。key 仅用于确定是否存在可判定的遮蔽对象，
// 以符文方式处理，CJK 键原样保留且不会被按字节截断。
func isKeyShadowed(key string, sources []string) (bool, string) {
	// 空键没有可判定的遮蔽对象，直接视为未遮蔽。TrimSpace 按 Unicode 处理空白，
	// 不会破坏 CJK 键本身。
	if strings.TrimSpace(key) == "" {
		return false, ""
	}

	var bestKind string
	var bestSource string
	for _, src := range sources {
		kind := shadowSourceKind(src)
		if kind == "" {
			continue
		}
		// flag 优先级高于 env；同级时取靠后的来源（列表按优先级从低到高）。
		if bestKind == "" || shadowRank(kind) >= shadowRank(bestKind) {
			bestKind, bestSource = kind, src
		}
	}
	if bestKind == "" {
		return false, ""
	}
	return true, bestSource
}

// shadowSourceKind 把单个来源标签归类为 env 或 flag，其他来源（配置文件路径、
// 编译默认值等）返回空串。识别规则：
//   - 忽略两端空白，大小写不敏感；
//   - 支持裸标签（"env"、"flag"）以及带说明的标签
//     （"env:ARGENT_MODEL"、"flag:--model"、"via flag"）；
//   - 同时识别中文标签（"环境变量"、"命令行"、"标志"）。
//
// 分隔符与前缀判断都基于 rune，因此 CJK 来源标签安全且不会被切成半个 UTF-8 字符。
func shadowSourceKind(source string) string {
	s := strings.TrimSpace(source)
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, prefix := range []string{"via ", "from ", "by "} {
		if strings.HasPrefix(lower, prefix) {
			lower = strings.TrimSpace(lower[len(prefix):])
			break
		}
	}
	head := lower
	if i := strings.IndexAny(lower, ":=(（> \t"); i >= 0 {
		head = strings.TrimSpace(lower[:i])
	}
	switch head {
	case "env", "environment", "envar", "环境", "环境变量":
		return shadowKindEnv
	case "flag", "flags", "cli", "argv", "命令行", "标志", "命令行标志", "命令行参数", "命令行选项":
		return shadowKindFlag
	}
	return ""
}

// shadowRank 返回遮蔽来源的优先级序号，用于稳定地选取最高优先级遮蔽项。
func shadowRank(kind string) int {
	if kind == shadowKindFlag {
		return 2
	}
	return 1
}

// configShadowCheckEnabled 报告配置写入前的遮蔽检查是否启用：默认开启，
// 在 config set 写入前提示"set 成功但运行值不变"的风险；仅当
// ARGENT_CONFIG_SHADOW_CHECK=0（允许首尾空白）时紧急关闭；env 为 nil 时按默认处理。
func configShadowCheckEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env("ARGENT_CONFIG_SHADOW_CHECK")) != "0"
}

// configShadowWarning 在检查启用且 key 被遮蔽时返回一条中文警告，否则返回空串。
// 它是纯函数，供 CLI 的 config set 在写入前调用，以提示
// "set 成功但运行值不变"的风险；检查被 =0 关闭时始终返回空串。
func configShadowWarning(key string, sources []string, env func(string) string) string {
	if !configShadowCheckEnabled(env) {
		return ""
	}
	shadowed, source := isKeyShadowed(key, sources)
	if !shadowed {
		return ""
	}
	return fmt.Sprintf("警告：键 %q 被更高优先级来源 %q 遮蔽；写入配置文件后运行值可能不变。", key, source)
}
