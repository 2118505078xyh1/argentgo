package cli

import (
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/server"
)

// dualProvider 同时实现 ConfigInfoProvider 与 StatusProvider，模拟
// internal/app 的默认适配器，用于验证 cli 侧的接口断言回退路径。
type dualProvider struct {
	body []byte
}

func (p dualProvider) ConfigInfo() server.ConfigInfo {
	return server.ConfigInfo{Model: "m"}
}

func (p dualProvider) StatusJSON() []byte { return p.body }

// onlyConfigProvider 只实现 ConfigInfoProvider，用于验证未升级的注入方
// 不会被误当成 StatusProvider。
type onlyConfigProvider struct{}

func (onlyConfigProvider) ConfigInfo() server.ConfigInfo {
	return server.ConfigInfo{Model: "m"}
}

// TestStatusProviderPrefersExplicit 验证显式注入的 StatusProvider 优先于
// ConfigInfoProvider 的断言回退。
func TestStatusProviderPrefersExplicit(t *testing.T) {
	explicit := staticCLIStatusProvider(`{"source":"explicit"}`)
	a := App{
		StatusProvider:     explicit,
		ConfigInfoProvider: dualProvider{body: []byte(`{"source":"fallback"}`)},
	}
	got := a.statusProvider()
	if got == nil || string(got.StatusJSON()) != `{"source":"explicit"}` {
		t.Fatalf("statusProvider() = %v, want explicit", got)
	}
}

// TestStatusProviderFallsBackToConfigInfo 验证未显式注入时，同时实现两个
// 接口的 ConfigInfoProvider 被复用为 StatusProvider——这是默认接线不吃
// cmd/argent 改动的关键。
func TestStatusProviderFallsBackToConfigInfo(t *testing.T) {
	fallback := dualProvider{body: []byte(`{"source":"fallback"}`)}
	a := App{ConfigInfoProvider: fallback}
	got := a.statusProvider()
	if got == nil || string(got.StatusJSON()) != `{"source":"fallback"}` {
		t.Fatalf("statusProvider() = %v, want fallback", got)
	}
}

// TestStatusProviderNilWhenAbsent 验证两者都不提供 status 能力时返回 nil：
// --serve 不注册 /v1/status，--rpc 的 status 返回干净错误，默认 wire 不变。
func TestStatusProviderNilWhenAbsent(t *testing.T) {
	cases := []App{
		{},
		{ConfigInfoProvider: onlyConfigProvider{}},
	}
	for i, a := range cases {
		if got := a.statusProvider(); got != nil {
			t.Fatalf("case %d: statusProvider() = %v, want nil", i, got)
		}
	}
}

// staticCLIStatusProvider 是 cli 测试内的固定提供者。
type staticCLIStatusProvider string

func (s staticCLIStatusProvider) StatusJSON() []byte { return []byte(s) }
