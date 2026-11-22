package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// patchPolicyPathsEnabled 报告 apply_patch 目标路径策略校验是否开启。
// 默认关闭：只有 ARGENT_PATCH_POLICY_PATHS=1 才启用，其他任何值都走原链路，
// 保证默认运行的工具调用与审计逐字节不变。
func patchPolicyPathsEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env("ARGENT_PATCH_POLICY_PATHS")) == "1"
}

// patchPolicyGuard 在 opt-in 开启时，把 apply_patch 的 input 正文解析成真实
// 目标文件列表，逐条走既有安全策略。它包裹在原有 guard（检查层/安全链）之前，
// 因此默认关闭时 inner 就是原 guard，整段逻辑被一个 bool 读取短路。
//
// 不在这里改 internal/tools → internal/security 的依赖方向：解析使用 tools 的
// 纯函数 ParseApplyPatch，策略判定复用 Chain 已持有的 Policy。
type patchPolicyGuard struct {
	inner tools.Guard
	chain *security.Chain
}

func (g patchPolicyGuard) Check(ctx context.Context, name string, args json.RawMessage) error {
	if g.chain == nil || !strings.EqualFold(strings.TrimSpace(name), "apply_patch") {
		return g.inner.Check(ctx, name, args)
	}
	var payload struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(args, &payload); err != nil || strings.TrimSpace(payload.Input) == "" {
		return g.inner.Check(ctx, name, args)
	}
	actions, err := tools.ParseApplyPatch(payload.Input)
	if err != nil || len(actions) == 0 {
		// 畸形补丁交给工具自身报告，避免守卫改变现有错误文案。
		return g.inner.Check(ctx, name, args)
	}
	paths := make([]string, 0, len(actions))
	for _, action := range actions {
		paths = append(paths, action.Path)
	}
	if err := g.chain.CheckPatchTargets(name, paths, args); err != nil {
		return err
	}
	return g.inner.Check(ctx, name, args)
}

// patchPolicyGuardFor 按 opt-in 决定是否包裹 guard。关闭时原样返回 inner。
func patchPolicyGuardFor(inner tools.Guard, chain *security.Chain, env func(string) string) tools.Guard {
	if !patchPolicyPathsEnabled(env) || inner == nil || chain == nil {
		return inner
	}
	return patchPolicyGuard{inner: inner, chain: chain}
}
