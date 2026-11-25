package app

import (
	"testing"

	"example.com/argentgo/internal/cli"
)

// Resolve 必须把别名拼写（大小写变体、下划线分隔符）规范化回内置模板的规范模型名，
// 使下游 llm 客户端拿到的始终是稳定的模型 id。
func TestResolveNormalizesModelAlias(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	writeArgentConfig(t, root, `{}`)

	res, err := Resolve(cli.IO{Env: isolatedEnv(home, nil)}, cli.Options{Cwd: root, Model: "glm_5.3"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Model != "GLM-5.3" {
		t.Fatalf("model = %q, want canonical %q", res.Model, "GLM-5.3")
	}
}
