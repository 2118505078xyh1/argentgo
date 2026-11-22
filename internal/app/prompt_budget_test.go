package app

import (
	"encoding/json"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// TestPromptBudget 把 killi 的"system prompt + 工具定义保持精简"纪律变成 CI 断言：
// 统计静态系统提示与工具注册表的工具 schema 总字节数，设一个宽松但明确的上限，
// 防止后续逐步膨胀导致缓存前缀变大、每轮请求变贵。
//
// 这里用字节数而非 token 数（零依赖、确定性）：ASCII 为主的提示里字节数与 token 数
// 近似线性，作为回归护栏足够；真正的 token 计量由 provider 报告负责。
func TestPromptBudget(t *testing.T) {
	root := t.TempDir()
	env := func(string) string { return "" }

	prompt := buildStaticSystem(root, env, true)
	registry := tools.New(root, false)
	specs := registry.Specs()

	total := len(prompt)
	for _, spec := range specs {
		encoded, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		total += len(encoded)
	}
	t.Logf("prompt budget: %d bytes (system %d + %d tool specs)", total, len(prompt), len(specs))

	// 上限 40000 字节约为当前实测值（约 21 KB）的两倍：既拦住数量级膨胀，
	// 又给合法增长留空间。这里只统计静态系统提示 + 内置工具注册表；
	// app 层注入的 MCP / skill 工具由各自测试覆盖。若确需突破，先评审缓存成本。
	const budget = 40000
	if total > budget {
		t.Fatalf("prompt + tools = %d bytes, exceeds budget %d", total, budget)
	}

	// 静态提示必须与上下文解耦：cwd 变化不得改变其字节（缓存前缀稳定的硬不变量）。
	otherRoot := t.TempDir()
	if got := buildStaticSystem(otherRoot, env, true); got != prompt {
		t.Fatal("buildStaticSystem changed with working directory; cache prefix would break")
	}

	// 工具目录在一轮之内必须逐字节稳定：连续两次 Specs() 必须相同。
	first, _ := json.Marshal(specs)
	second, _ := json.Marshal(registry.Specs())
	if string(first) != string(second) {
		t.Fatal("registry.Specs() is not byte-stable across calls")
	}
}
