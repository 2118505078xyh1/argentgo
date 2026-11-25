package app

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"example.com/argentgo/internal/mcp"
)

// TestPluginServerDir 验证插件 bundle 的 Dir 被传入 stdio 服务器规格，
// 使相对路径脚本按 bundle 目录解析；没有 bundle 的配置服务器仍保持空 Dir。
func TestPluginServerDir(t *testing.T) {
	root := t.TempDir()
	// 用 CJK 目录名覆盖插件工作目录的非 ASCII 路径。
	pluginDir := filepath.Join(root, ".argent", "plugins", "插件目录")
	writeFile(t, filepath.Join(pluginDir, "plugin.json"), `{
		"name":"cjkplugin",
		"mcpServers":{"cjk":{"command":"cjk-server"}}
	}`)
	// 用户配置的服务没有 bundle 根，Dir 应保持为零值。
	writeFile(t, filepath.Join(root, "argent.json"),
		`{"mcpServers":{"configured":{"command":"config-server"}}}`)

	type startedServer struct {
		name string
		dir  string
	}
	var seen []startedServer
	useFakeMCP(t, func(_ context.Context, spec mcp.Spec) (mcp.Client, error) {
		seen = append(seen, startedServer{name: spec.Name, dir: spec.Dir})
		return &fakeMCPClient{tools: []mcp.Tool{{Name: "run"}}}, nil
	})

	_ = newSecurityToolset(t, root, true, &bytes.Buffer{})

	got := make(map[string]string, len(seen))
	for _, s := range seen {
		got[s.name] = s.dir
	}
	if dir := got["cjk"]; dir != pluginDir {
		t.Fatalf("plugin server Dir = %q, want bundle dir %q", dir, pluginDir)
	}
	if dir, ok := got["configured"]; !ok || dir != "" {
		t.Fatalf("configured server Dir = %q (present=%v), want empty", dir, ok)
	}
}
