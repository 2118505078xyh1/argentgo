package app

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/provider"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestModelsToolsEnabled(t *testing.T) {
	cases := []struct {
		env  func(string) string
		want bool
	}{
		{nil, true},
		{func(string) string { return "" }, true},
		{func(string) string { return "0" }, false},
		{func(string) string { return " 0 " }, false},
	}
	for _, tc := range cases {
		if got := modelsToolsEnabled(tc.env); got != tc.want {
			t.Errorf("modelsToolsEnabled = %v, want %v", got, tc.want)
		}
	}
}

func TestSafeModelInfosHasNoSecrets(t *testing.T) {
	infos := safeModelInfos()
	if len(infos) == 0 {
		t.Fatal("provider.List() returned no templates")
	}
	// 安全子集只有四个字段，不含 API key / 环境变量名。
	for _, m := range infos {
		if m.Name == "" {
			t.Error("empty template name")
		}
	}
	// 与 CLI 口径一致：条目数等于 provider.List()。
	if len(infos) != len(provider.List()) {
		t.Fatalf("infos = %d, templates = %d", len(infos), len(provider.List()))
	}
}

func TestWireModelsInstallsTool(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}
	wireModels(cli.IO{}, set)
	wireModels(cli.IO{}, set) // 幂等
	count := 0
	for _, name := range r.Names() {
		if name == "models" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("models registered %d times", count)
	}

	// 关闭时不注册。
	r2 := tools.New(t.TempDir(), true)
	set2 := &toolset{base: &multiRootTools{regs: []*tools.Registry{r2}}}
	wireModels(cli.IO{Env: func(string) string { return "0" }}, set2)
	for _, name := range r2.Names() {
		if name == "models" {
			t.Fatal("models registered despite disable")
		}
	}
}

// 描述静态/离线的断言放在 tools 包模型中，避免跨包访问私有类型。
