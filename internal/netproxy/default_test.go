package netproxy

import (
	"context"
	"fmt"
	"testing"

	"example.com/argentgo/internal/security"
)

// TestDefaultEnvIsolatesChains 守护 P0-1 的跨会话串味：不同审批链必须拿到
// 不同的代理实例（不同端口），否则 A 会话的策略会被 B 会话复用、首次 ctx
// 的策略被永久定格。同一条链应复用实例。
func TestDefaultEnvIsolatesChains(t *testing.T) {
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)

	chainA := security.NewChain(nil, nil, nil, "session-A")
	chainB := security.NewChain(nil, nil, nil, "session-B")
	ctxA := security.WithNetworkGuard(context.Background(), security.NewNetworkGuard(nil, chainA))
	ctxB := security.WithNetworkGuard(context.Background(), security.NewNetworkGuard(nil, chainB))

	envA := DefaultEnv(ctxA)
	envB := DefaultEnv(ctxB)
	if envA.IsZero() || envB.IsZero() {
		t.Fatalf("default env must not be zero: A=%+v B=%+v", envA, envB)
	}
	if envA.HTTPProxy == envB.HTTPProxy {
		t.Fatalf("different chains must get different proxy addresses, both %q", envA.HTTPProxy)
	}

	// 再次用同一条链必须命中缓存，地址不变。
	if again := DefaultEnv(ctxA); again.HTTPProxy != envA.HTTPProxy {
		t.Fatalf("same chain must reuse cached proxy: %q vs %q", again.HTTPProxy, envA.HTTPProxy)
	}
}

// TestDefaultEnvFailClosed 守护 P0-1 的 fail-open：代理启动失败时绝不返回
// 空 Env（那会让子进程直连绕过沙箱），而要返回指向不可连接地址的 Env，
// 使出网失败而不是静默放行。
func TestDefaultEnvFailClosed(t *testing.T) {
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)

	newProxy = func(Options) (*Proxy, error) {
		return nil, fmt.Errorf("listen: simulated port exhaustion")
	}

	env := DefaultEnv(context.Background())
	if env.IsZero() {
		t.Fatal("proxy startup failure must NOT yield empty env (would direct-connect)")
	}
	if env.HTTPProxy != "http://127.0.0.1:0" || env.HTTPSProxy != "http://127.0.0.1:0" {
		t.Fatalf("fail-closed env must point to unconnectable address, got %+v", env)
	}
}

// TestDefaultEnvLimitedSeparateInstance 验证只读标志参与缓存 key：只读会话
// 与普通会话使用不同代理，避免 limited 标志被首个 ctx 定格。
func TestDefaultEnvLimitedSeparateInstance(t *testing.T) {
	resetDefaultForTest()
	t.Cleanup(resetDefaultForTest)

	t.Setenv("ARGENT_NETPROXY_LIMITED", "")
	normal := DefaultEnv(context.Background())
	t.Setenv("ARGENT_NETPROXY_LIMITED", "1")
	limited := DefaultEnv(context.Background())
	if normal.IsZero() || limited.IsZero() {
		t.Fatalf("env must not be zero: normal=%+v limited=%+v", normal, limited)
	}
	if normal.HTTPProxy == limited.HTTPProxy {
		t.Fatalf("limited mode must use a separate proxy, both %q", normal.HTTPProxy)
	}
}
