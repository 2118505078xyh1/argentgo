package app

import (
	"strings"
	"unicode"
)

// ResolveStage 表示 Resolve 装配流程中的一个阶段。
// 它用于给未来拆分 Resolve 时的字段归属建立一张地图：每个配置键/字段名都归到某一个阶段，
// 本文件只提供纯函数原语，不接任何调用方，也不改变 Resolve 现有的行为与签名。
type ResolveStage string

const (
	// StageConfig 是兜底阶段：所有未被前缀规则命中的字段（含未知字段）都归到这里。
	StageConfig ResolveStage = "config"
	// StageProvider 覆盖模型、提供方、凭据、协议与线路相关的字段。
	StageProvider ResolveStage = "provider"
	// StagePricing 覆盖价格与预算上限相关的字段。
	StagePricing ResolveStage = "pricing"
	// StageSession 覆盖工作区、会话存储与会话产物相关的字段。
	StageSession ResolveStage = "session"
	// StageHooks 覆盖钩子及其各类事件相关的字段。
	StageHooks ResolveStage = "hooks"
)

// resolveStageRules 是按优先级排列的前缀规则表。
// 这里刻意让钩子规则排在会话规则之前，使 "sessionStart"/"sessionEnd" 这类钩子事件
// 不会被更宽泛的 "session" 前缀抢走；因此本表的优先级与 StageOrder 的依赖顺序不同——
// 前者解决歧义，后者描述装配顺序。
var resolveStageRules = []struct {
	stage    ResolveStage
	prefixes []string
}{
	{StageHooks, []string{"hook", "pretool", "posttool", "notification", "stop", "subagentstop", "precompact", "sessionstart", "sessionend"}},
	{StageProvider, []string{"provider", "protocol", "baseurl", "endpoint", "model", "fallback", "secondary", "apikey", "chatter"}},
	{StagePricing, []string{"price", "prices", "pricing", "cost", "costs", "maxcost"}},
	{StageSession, []string{"session", "root", "home", "storage", "workspace", "cwd", "adddir", "writeroot", "outputschema", "system", "resume", "undo", "retry", "title"}},
}

// normalizeResolveKey 归一化配置键：去掉常见分隔符、统一大小写，使 kebab-case、snake_case
// 与 camelCase 写法等价。按 rune 遍历，因此含 CJK 的键也能被正确处理而不会截断多字节字符。
func normalizeResolveKey(key string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', ' ', '.', '/', ':':
			return -1
		default:
			return unicode.ToLower(r)
		}
	}, strings.TrimSpace(key))
}

// ClassifyResolveField 把一个配置键/字段名归入某个装配阶段。
// 它先归一化键，再按固定优先级匹配前缀；未知字段一律归入 StageConfig。
// 这是纯函数：不查询配置、环境或任何外部状态，结果只取决于入参。
func ClassifyResolveField(key string) ResolveStage {
	normalized := normalizeResolveKey(key)
	for _, rule := range resolveStageRules {
		for _, prefix := range rule.prefixes {
			if strings.HasPrefix(normalized, prefix) {
				return rule.stage
			}
		}
	}
	return StageConfig
}

// GroupByStage 按阶段把键分组。每个阶段内的切片保持输入中的相对顺序（稳定），
// 且都是新建的副本：调用方修改结果不会影响入参，也不会影响后续调用。
// 入参本身不会被改动；没有命中任何键的阶段不会出现在结果里。
func GroupByStage(keys []string) map[ResolveStage][]string {
	out := make(map[ResolveStage][]string)
	for _, key := range keys {
		stage := ClassifyResolveField(key)
		out[stage] = append(out[stage], key)
	}
	return out
}

// StageOrder 返回阶段化的固定装配顺序：config → provider → pricing → session → hooks。
// 每次调用都返回一个新的切片，调用方可以安全地修改它而不会污染后续调用。
func StageOrder() []ResolveStage {
	return []ResolveStage{StageConfig, StageProvider, StagePricing, StageSession, StageHooks}
}
