package app

import (
	"errors"
	"fmt"
	"strings"

	"example.com/argentgo/internal/pluginapi"
)

// bootTopoEnv 是启动拓扑校验的环境变量名。只有设为 "1" 才启用；
// 默认关闭时完全不调用 TopoSort，boot 八步保持历史字面顺序，逐字节不变。
const bootTopoEnv = "ARGENT_BOOT_TOPO"

// bootFeatureNames 是 Execute 中 boot 八步的历史顺序（对应 app_execute.go 的接线序列）。
// 它是校验的"黄金基准"：任何依赖声明的推导结果必须与此逐项相等。
var bootFeatureNames = []string{
	"hooks", "approver", "audit", "policy", "chain", "guard", "toolset", "runner",
}

// bootTopoEnabled 报告是否启用启动拓扑校验。默认关闭：
// 只有 ARGENT_BOOT_TOPO=1（允许首尾空白）才启用，风格与 inspectionEnabled 一致。
func bootTopoEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(bootTopoEnv)) == "1"
}

// bootFeatures 声明 boot 八步的数据依赖与历史次序。
//
// Deps 决定分层（谁先于谁构造），Order 决定同层内的先后。二者共同保证
// TopoSort 的输出与 bootFeatureNames 逐项相等：
//   - hooks/approver/audit/policy 无依赖，位于第 0 层，按 Order 排成 hooks→approver→audit→policy；
//   - chain 依赖前三者，位于第 1 层；guard 依赖 chain（第 2 层）；
//   - toolset 依赖 guard（第 3 层）；runner 依赖 toolset 与 hooks（第 4 层）。
//
// 依赖边锁死了关键顺序约束：chain/guard 必须早于 toolset（否则只读 MCP 策略回写丢失、
// guard 具体类型未定型），hooks 必须早于 runner（否则 hook 包装缺失）。
func bootFeatures() []pluginapi.Feature {
	return []pluginapi.Feature{
		{Name: "hooks", Order: 0},
		{Name: "approver", Order: 1},
		{Name: "audit", Order: 2},
		{Name: "policy", Order: 3},
		{Name: "chain", Deps: []string{"approver", "audit", "policy"}, Order: 4},
		{Name: "guard", Deps: []string{"chain"}, Order: 5},
		{Name: "toolset", Deps: []string{"guard"}, Order: 6},
		{Name: "runner", Deps: []string{"toolset", "hooks"}, Order: 7},
	}
}

// ErrBootTopoDrift 表示推导出的启动顺序与历史顺序不一致。这是 fail-closed 的启动期错误：
// 宁可拒绝启动，也不让一处遗漏的依赖声明静默改变构造顺序。
var ErrBootTopoDrift = errors.New("boot topology drift")

// bootOrder 校验启动拓扑。默认（env 未设或 !=1）直接返回历史顺序，不调用 TopoSort；
// 启用时用依赖图推导顺序，并与历史顺序逐项比对——相等才放行，不等返回 ErrBootTopoDrift。
//
// 该函数只做"验证"，不改变 Execute 的构造代码：boot 八步仍按历史字面顺序执行。
// 它让 TopoSort 原语真正接线到启动路径，同时以相等断言保证零行为漂移。
func bootOrder(env func(string) string) error {
	if !bootTopoEnabled(env) {
		return nil
	}
	features := bootFeatures()
	// 三个原语都接到同一校验点：先校验依赖声明合法（重名/缺失/环），
	// 再核对推导顺序与历史顺序逐项一致；漂移错误附 StableKey 稳定诊断。
	if err := pluginapi.ValidateFeatureDeps(features); err != nil {
		return fmt.Errorf("boot topology: %w", err)
	}
	if err := pluginapi.ExpectedOrder(features, bootFeatureNames); err != nil {
		keys := make([]string, 0, len(features))
		for _, f := range features {
			keys = append(keys, pluginapi.StableKey(f))
		}
		return fmt.Errorf("%w: %v (features: %v)", ErrBootTopoDrift, err, keys)
	}
	return nil
}
