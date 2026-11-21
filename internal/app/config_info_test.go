package app

import (
	"errors"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/server"
)

// TestNewConfigInfoProviderMapsResolvedFields 验证适配器把 Resolved 的
// Model/Provider/Root 精确映射到 ConfigInfo 的三个 wire 字段。resolve 被
// 注入，因此测试不依赖进程工作目录或磁盘配置。
func TestNewConfigInfoProviderMapsResolvedFields(t *testing.T) {
	resolve := func(cli.IO, cli.Options) (Resolved, error) {
		return Resolved{Model: "gpt-4o", Provider: "openai", Root: `D:\work\demo`}, nil
	}
	provider := newConfigInfoProvider(cli.IO{Env: func(string) string { return "" }}, resolve)
	got := provider.ConfigInfo()
	want := server.ConfigInfo{Model: "gpt-4o", Provider: "openai", Workspace: `D:\work\demo`}
	if got != want {
		t.Fatalf("ConfigInfo() = %+v, want %+v", got, want)
	}
}

// TestNewConfigInfoProviderToleratesResolveError 验证配置解析失败时返回
// 零值适配器而非 panic：状态栏会回出空摘要，不影响其余 RPC 方法。
func TestNewConfigInfoProviderToleratesResolveError(t *testing.T) {
	resolve := func(cli.IO, cli.Options) (Resolved, error) {
		return Resolved{}, errors.New("load config: boom")
	}
	provider := newConfigInfoProvider(cli.IO{}, resolve)
	if got := provider.ConfigInfo(); got != (server.ConfigInfo{}) {
		t.Fatalf("ConfigInfo() = %+v, want zero value", got)
	}
}

// TestNewConfigInfoProviderUsesDefaultResolve 验证公开构造函数确实接入了
// 真实的 Resolve：默认值下至少能拿到非空的 model 与 workspace。
func TestNewConfigInfoProviderUsesDefaultResolve(t *testing.T) {
	provider := NewConfigInfoProvider(cli.IO{Env: func(string) string { return "" }})
	info := provider.ConfigInfo()
	if info.Model == "" {
		t.Error("Model is empty; NewConfigInfoProvider did not use Resolve")
	}
	if info.Workspace == "" {
		t.Error("Workspace is empty; NewConfigInfoProvider did not use Resolve")
	}
}
