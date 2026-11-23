package netproxy

import (
	"context"
	"os"
	"strings"
)

// Env 是要注入子进程的环境变量集合。它只描述代理地址，不改变 shell 本体。
type Env struct {
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
}

// IsZero 报告是否没有可注入的值。
func (e Env) IsZero() bool {
	return e.HTTPProxy == "" && e.HTTPSProxy == "" && e.NoProxy == ""
}

// Entries 返回 "KEY=VALUE" 形式的条目。大小写两种写法都写入：curl 读取
// 小写变体，Go/Java 通常读取大写变体，重复键由 os/exec 保留最后一个值。
func (e Env) Entries() []string {
	var out []string
	add := func(name, value string) {
		if value == "" {
			return
		}
		out = append(out, name+"="+value, strings.ToLower(name)+"="+value)
	}
	add("HTTP_PROXY", e.HTTPProxy)
	add("HTTPS_PROXY", e.HTTPSProxy)
	add("NO_PROXY", e.NoProxy)
	return out
}

// Apply 把代理条目追加到基础环境，返回新的切片（不修改入参）。
// 追加在尾部使这些值优先于继承的宿主代理设置。
func (e Env) Apply(base []string) []string {
	return append(append([]string(nil), base...), e.Entries()...)
}

// envKey 是 Env 的私有 context key。
type envKey struct{}

// WithEnv 把代理环境注入 ctx，供 bash 工具在启动子进程时读取。
func WithEnv(ctx context.Context, env Env) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, envKey{}, env)
}

// EnvFrom 返回 ctx 中显式注入的代理环境；未注入时 ok 为 false。
func EnvFrom(ctx context.Context) (Env, bool) {
	if ctx == nil {
		return Env{}, false
	}
	env, ok := ctx.Value(envKey{}).(Env)
	return env, ok
}

// disabled 报告紧急回退开关：只有显式设为 "0" 才关闭默认代理注入。
func disabled() bool { return strings.TrimSpace(os.Getenv("ARGENT_NETPROXY")) == "0" }
