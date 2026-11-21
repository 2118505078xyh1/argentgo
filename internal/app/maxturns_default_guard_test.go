package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
)

// R16 行为改变：默认 maxTurns 从 12 抬高到 100（与 agent.DefaultMaxTurns 对齐）。
// 本文件是守护测试：确保默认值本身对齐，且所有既有覆盖渠道依旧优先于兜底值。
// 这些断言引用 agent.DefaultMaxTurns / 字面量 100（期望值，依赖默认值改动）。

// TestMaxTurnsDefaultGuardEngineConstant 锁定引擎常量的期望数值。
// 期望值（依赖默认值改动）：R16 中由 12 改为 100。
func TestMaxTurnsDefaultGuardEngineConstant(t *testing.T) {
	if agent.DefaultMaxTurns != 100 {
		t.Fatalf("agent.DefaultMaxTurns = %d, want 100", agent.DefaultMaxTurns)
	}
}

// TestMaxTurnsDefaultGuardResolveUsesEngineDefault 断言无配置、无环境变量、
// 无显式选项时，Resolve 的默认轮次预算等于引擎默认值。
func TestMaxTurnsDefaultGuardResolveUsesEngineDefault(t *testing.T) {
	root := t.TempDir()
	res, err := Resolve(cli.IO{Env: isolatedEnv(t.TempDir(), nil)}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.MaxTurns != agent.DefaultMaxTurns {
		t.Fatalf("MaxTurns = %d, want engine default %d", res.MaxTurns, agent.DefaultMaxTurns)
	}
	// 期望值（依赖默认值改动）：显式锁定 100，防止常量与字面量同时漂移。
	if res.MaxTurns != 100 {
		t.Fatalf("MaxTurns = %d, want 100", res.MaxTurns)
	}
}

// TestMaxTurnsDefaultGuardOverridesStillWin 验证抬高默认值没有剥夺任何既有覆盖渠道：
// 显式选项 > ARGENT_MAX_TURNS / -c maxTurns / 配置文件 / profile > 兜底默认值。
func TestMaxTurnsDefaultGuardOverridesStillWin(t *testing.T) {
	cases := []struct {
		name   string
		config string // 非空时写入 root/argent.json
		env    map[string]string
		opts   cli.Options // 只使用 MaxTurns / Profile 字段
		want   int
	}{
		{
			name: "no overrides uses engine default",
			want: 100,
		},
		{
			name: "ARGENT_MAX_TURNS env overrides default",
			env:  map[string]string{"ARGENT_MAX_TURNS": "7"},
			want: 7,
		},
		{
			name:   "config maxTurns overrides default",
			config: `{"maxTurns":8}`,
			want:   8,
		},
		{
			name:   "profile maxTurns overrides default",
			config: `{"profile":"work","profiles":{"work":{"maxTurns":9}}}`,
			opts:   cli.Options{Profile: "work"},
			want:   9,
		},
		{
			name: "explicit option overrides default",
			opts: cli.Options{MaxTurns: 11},
			want: 11,
		},
		{
			name:   "env beats config",
			config: `{"maxTurns":8}`,
			env:    map[string]string{"ARGENT_MAX_TURNS": "7"},
			want:   7,
		},
		{
			name:   "explicit option beats env and config",
			config: `{"maxTurns":8}`,
			env:    map[string]string{"ARGENT_MAX_TURNS": "7"},
			opts:   cli.Options{MaxTurns: 11},
			want:   11,
		},
		{
			name:   "zero config still falls back to engine default",
			config: `{"maxTurns":0}`,
			want:   100,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.config != "" {
				writeArgentConfig(t, root, tc.config)
			}
			opts := tc.opts
			opts.Cwd = root
			res, err := Resolve(cli.IO{Env: isolatedEnv(t.TempDir(), tc.env)}, opts)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.MaxTurns != tc.want {
				t.Fatalf("MaxTurns = %d, want %d", res.MaxTurns, tc.want)
			}
		})
	}
}

// TestMaxTurnsDefaultGuardPromptDeclarationStacksSeparately 验证提示词中的
// `+500k` 声明仍是叠加的累计 token 预算，不会改写已解析出的默认轮次上限。
func TestMaxTurnsDefaultGuardPromptDeclarationStacksSeparately(t *testing.T) {
	root := t.TempDir()
	res, err := Resolve(cli.IO{Env: isolatedEnv(t.TempDir(), nil)}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	b := agent.Budget{
		MaxTurns:        res.MaxTurns,
		MaxPromptTokens: 100,
		MaxTotalTokens:  1_000,
	}
	applyDeclaredTurnBudget("+500k", &b)
	if b.DeclaredTurnTokens != 500_000 {
		t.Fatalf("DeclaredTurnTokens = %d, want 500000", b.DeclaredTurnTokens)
	}
	if b.MaxTurns != res.MaxTurns {
		t.Fatalf("MaxTurns = %d, want unchanged %d", b.MaxTurns, res.MaxTurns)
	}
	if b.MaxPromptTokens != 100 || b.MaxTotalTokens != 1_000 {
		t.Fatalf("other budget fields changed: %+v", b)
	}
}
