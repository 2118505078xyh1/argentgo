package netproxy

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 进程级默认代理：自研 shell 的子进程默认就走这层出网检查，无需用户配置。
// 只有 ARGENT_NETPROXY=0 才紧急关闭。显式 WithEnv 注入的 Env 优先于它。

var (
	defaultOnce   sync.Once
	defaultProxy  *Proxy
	defaultEnvVal Env
)

// DefaultEnv 懒启动默认代理并返回其注入环境。代理监听 127.0.0.1 的随机
// 端口；审批链取自首次调用 ctx 里注入的 NetworkGuard（若有）。
//
// 只读profile（或 ARGENT_NETPROXY_LIMITED=1）会让代理进入 Limited 模式，
// 只允许 GET/HEAD/OPTIONS；这与安全链"只收紧不放宽"的语义一致。
func DefaultEnv(ctx context.Context) Env {
	if disabled() {
		return Env{}
	}
	defaultOnce.Do(func() {
		var chain *security.Chain
		if guard := security.NetworkGuardFrom(ctx); guard != nil {
			chain = guard.Chain
		}
		limited := security.CallProfileFrom(ctx).ReadOnly ||
			strings.TrimSpace(os.Getenv("ARGENT_NETPROXY_LIMITED")) == "1"
		proxy, err := New(Options{Chain: chain, Limited: limited})
		if err != nil {
			return
		}
		defaultProxy = proxy
		defaultEnvVal = proxy.Env()
	})
	return defaultEnvVal
}
