package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/auth"
)

// 设备码登录的可注入接缝。生产环境直接使用 auth 包的真实实现；测试替换这两个
// 变量即可覆盖完整的 CLI 接线（预设查找、用户码打印、轮询、令牌入库），而无需
// 真正访问网络。
var (
	deviceStart = auth.DeviceStart
	devicePoll  = auth.DevicePoll
)

// deviceLoginTimeout 是设备流程的最长等待时间。它与 auth 包默认的代码有效期
// （15 分钟）一致；context 超时会让 DevicePoll 的等待循环立即退出。
const deviceLoginTimeout = 15 * time.Minute

// runDeviceLogin 实现 `login <provider> --device`：用 RFC 8628 设备码流程登录，
// 先把用户码和验证地址显示给用户，再轮询授权结果，最后把令牌写入现有凭证库。
func runDeviceLogin(app App, parent context.Context, provider string) int {
	// 只接受声明了设备流程的预设，避免把 API key 或浏览器流程误当成设备流程。
	preset, ok := auth.Preset(provider)
	if !ok || preset.Kind != auth.KindDevice {
		fmt.Fprintf(app.IO.Err, "error: provider %q does not support device login\n", provider)
		deviceLoginUsage(app.IO.Err)
		return 2
	}

	ctx, cancel := context.WithTimeout(parent, deviceLoginTimeout)
	defer cancel()

	started, err := deviceStart(ctx, preset.Config)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}

	// 只显示公开的 user_code 与 verification_uri；device_code 与令牌绝不回显。
	fmt.Fprintf(app.IO.Out, "device login for %s\n", provider)
	fmt.Fprintf(app.IO.Out, "open %s and enter code %s\n", started.VerificationURI, started.UserCode)
	if started.Interval > 0 {
		fmt.Fprintf(app.IO.Out, "waiting for authorization (polling every %ds, expires in %ds)\n",
			started.Interval, started.ExpiresIn)
	}

	token, err := devicePoll(ctx, preset.Config, started)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}

	store, err := authStore(app)
	if err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	if err := store.Put(token); err != nil {
		fmt.Fprintln(app.IO.Err, "error:", err)
		return 1
	}
	// 成功信息只含提供方名称与存储路径，不含任何密钥。
	fmt.Fprintf(app.IO.Out, "saved device token for %s in %s\n", token.Provider, store.Path())
	return 0
}

func deviceLoginUsage(w io.Writer) {
	fmt.Fprint(w, `usage: argentgo login <provider> --device

Runs the RFC 8628 device-code flow for a provider that supports it (for
example github-copilot). The user code and verification URL are printed, then
argentgo polls until the authorization completes. The resulting token is stored
in <storage>/auth; secrets are never echoed.
`)
}
