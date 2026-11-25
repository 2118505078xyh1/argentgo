package netproxy

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"

	"example.com/argentgo/internal/security"
)

// 进程级默认代理：自研 shell 的子进程默认就走这层出网检查，无需用户配置。
// 只有 ARGENT_NETPROXY=0 才紧急关闭。显式 WithEnv 注入的 Env 优先于它。
//
// 代理按 (审批链, 只读标志) 缓存：不同会话的链各自持有独立的代理实例，
// 避免首次触发的 ctx 把策略永久定格、让 A 会话的审批链被 B 会话复用
// （docs/TODO_REVIEW_2026_11_23.md P0-1）。链为空时所有调用共享一个实例，
// 与无 NetworkGuard 注入的现状一致。

type defaultKey struct {
	chain   *security.Chain
	limited bool
}

var (
	defaultMu      sync.Mutex
	defaultProxies = make(map[defaultKey]*Proxy)
	// newProxy 是 New 的可替换入口，供测试注入启动失败以验证 fail-closed。
	newProxy = New
)

// DefaultEnv 懒启动默认代理并返回其注入环境。代理监听 127.0.0.1 的随机
// 端口；审批链与只读标志取自当前 ctx 注入的 NetworkGuard / CallProfile，
// 按组合缓存独立实例，确保不同策略的会话互不串味。
//
// 代理启动失败时返回指向不可连接地址的 fail-closed Env（子进程出网会失败
// 而不是静默直连绕过沙箱），并通过日志输出原因。紧急开关 ARGENT_NETPROXY=0
// 时返回空 Env——那是用户显式放弃保护，不属于静默降级。
func DefaultEnv(ctx context.Context) Env {
	if disabled() {
		return Env{}
	}
	var chain *security.Chain
	if guard := security.NetworkGuardFrom(ctx); guard != nil {
		chain = guard.Chain
	}
	limited := security.CallProfileFrom(ctx).ReadOnly ||
		strings.TrimSpace(os.Getenv("ARGENT_NETPROXY_LIMITED")) == "1"

	key := defaultKey{chain: chain, limited: limited}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if proxy, ok := defaultProxies[key]; ok {
		return proxy.Env()
	}
	proxy, err := newProxy(Options{Chain: chain, Limited: limited})
	if err != nil {
		return failClosedEnv(err)
	}
	defaultProxies[key] = proxy
	return proxy.Env()
}

// failClosedEnv 返回一个指向不可连接本地地址的 Env：子进程尝试出网时连接
// 代理会失败，从而阻断出网而不是绕过沙箱直连。NO_PROXY 保留回环直连（本地
// 服务本来就不经代理）。同时输出诊断，避免"策略被静默绕过而用户不知情"。
func failClosedEnv(err error) Env {
	log.Printf("netproxy: 默认代理启动失败，网络出口将被阻断（fail-closed）：%v", err)
	const dead = "http://127.0.0.1:0" // 0 端口永远不可连接
	return Env{HTTPProxy: dead, HTTPSProxy: dead, NoProxy: "localhost,127.0.0.1,::1"}
}

// resetDefaultForTest 清空缓存并恢复 newProxy，仅供测试使用。
func resetDefaultForTest() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	for _, proxy := range defaultProxies {
		_ = proxy.Close()
	}
	defaultProxies = make(map[defaultKey]*Proxy)
	newProxy = New
}
