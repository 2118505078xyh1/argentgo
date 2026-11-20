package tools_test

import (
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// TestRepoMapRespectIgnoreOptIn 验证 respect_ignore 是可选的（默认关闭），
// 开启后会用 .argentignore 模式从地图中排除敏感文件，而默认输出保持不变。
func TestRepoMapRespectIgnoreOptIn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".argentignore", "*.env\nsecret/\n")
	writeFile(t, root, ".env", "SECRET=1\n")
	writeFile(t, root, "main.go", "package main\n")
	writeFile(t, root, "config/app.env", "TOKEN=1\n")
	writeFile(t, root, "config/keep.go", "package config\n")
	writeFile(t, root, "secret/key.pem", "PRIVATE\n")

	registry := tools.New(root, true)

	// 默认（未开启）时输出不被过滤，保持原有行为字节不变。
	plain := mustRun(t, registry, "repo_map", `{"symbols":false}`)
	for _, want := range []string{".env", "config/app.env", "secret/key.pem"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("default repo_map should keep %q, got:\n%s", want, plain)
		}
	}

	// 开启后，被 .argentignore 排除的路径不再出现。
	filtered := mustRun(t, registry, "repo_map", `{"symbols":false,"respect_ignore":true}`)
	for _, unwanted := range []string{".env", "config/app.env", "secret/key.pem", "secret/"} {
		if strings.Contains(filtered, unwanted) {
			t.Fatalf("repo_map with respect_ignore included %q:\n%s", unwanted, filtered)
		}
	}
	for _, want := range []string{"main.go", "config/keep.go"} {
		if !strings.Contains(filtered, want) {
			t.Fatalf("repo_map with respect_ignore missing %q:\n%s", want, filtered)
		}
	}
}
