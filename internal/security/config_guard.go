package security

import (
	"encoding/json"
	"path/filepath"
	"strings"
)

// 本文件实现两项默认开启的路径守卫，都是纯文本匹配、只用标准库：
//   - 配置/说明文件编辑保护（#21）：命中 AGENTS.md / CLAUDE.md / config.json(c)
//     与 .argent/ 的修改类调用强制询问，且"始终允许"不得被持久化；
//   - 宽泛 read 规则收窄（#22）：read 命中 *.env / *.env.*（排除 *.env.example）
//     时把 Allow 降级为 Ask，避免密钥静默进入上下文与会话记录。

// configGuardNames 是必须保护的说明/配置文件名。改写它们会把指令或放宽的
// 策略持久化到下一次会话，属于跨会话污染而不是一次性越权。
var configGuardNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	"config.json",
	"config.jsonc",
}

// configGuardDirs 是保留的配置目录段。.argent 已在 DefaultProtectedNames 中
// 被写保护；这里再次列出，使"不可 always"判定也能覆盖放开了 writeRoots 的
// 调用方（保护语义与写边界解耦）。
var configGuardDirs = []string{".argent"}

// configFileGuard 报告一次修改类调用是否命中配置/说明文件。
//
// 只读工具（如 read AGENTS.md）是正常用法，刻意不守卫，否则每次读取项目规则
// 都要人工确认。判定只看已知路径参数的基名与保留目录段，大小写不敏感；
// 参数畸形时返回 false（安全假阴性，策略链仍会按原有分支落到 Ask）。
func configFileGuard(name string, args json.RawMessage) bool {
	if !MutatingTool(strings.TrimSpace(name)) {
		return false
	}
	fields, err := parseArgs(args)
	if err != nil {
		return false
	}
	for _, path := range pathValues(fields) {
		if isConfigGuardPath(path) {
			return true
		}
	}
	return false
}

// isConfigGuardPath 判断单个路径是否命中受保护的说明/配置文件或目录。
func isConfigGuardPath(path string) bool {
	clean := filepath.Clean(expandHome(strings.TrimSpace(path)))
	base := filepath.Base(clean)
	for _, name := range configGuardNames {
		if strings.EqualFold(base, name) {
			return true
		}
	}
	for _, segment := range pathSegments(clean) {
		for _, dir := range configGuardDirs {
			if segmentsEqual(segment, dir) {
				return true
			}
		}
	}
	return false
}

// narrowBroadReadSecret 把"宽泛 read 规则命中密钥文件"的 Allow 降级为 Ask。
//
// 仅作用于 read 工具；显式 DenyPaths 已在 checkPath 阶段先返回 Deny，优先级
// 天然高于本收窄。命中时返回 Ask 而不是 Deny：操作员仍可在确有需要时放行，
// 但 --yolo/auto 下不会再静默把 .env.local 读进上下文。
func narrowBroadReadSecret(name string, fields map[string]any, res Result) Result {
	if name != "read" || res.Decision != Allow {
		return res
	}
	for _, path := range pathValues(fields) {
		if isSecretEnvPath(path) {
			return Result{Ask, "the read call targets a secret environment file; operator approval required"}
		}
	}
	return res
}

// isSecretEnvPath 按文件名（大小写不敏感）匹配 *.env 与 *.env.*，
// 但放行 *.env.example 示例文件。裸 .env 也命中。
func isSecretEnvPath(path string) bool {
	base := strings.ToLower(filepath.Base(expandHome(strings.TrimSpace(path))))
	if strings.HasSuffix(base, ".env.example") {
		return false
	}
	if base == ".env" || strings.HasSuffix(base, ".env") {
		return true
	}
	return strings.Contains(base, ".env.")
}
