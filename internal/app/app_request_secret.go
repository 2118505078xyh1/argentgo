package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/auth"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// 本文件把 request_secret 工具接到 app 装配面（TODO #18）。
// 值由 textSecretRequester（或 TUI 注入的模态 requester）收集，经 authSecretSink
// 写入既有凭据存储并暴露为同名进程环境变量；工具结果只有状态 + 名字，
// 密钥字节不进入会话 JSONL、事件流或模型上下文。默认开启，ARGENT_REQUEST_SECRET=0
// 仅作紧急回退。

const requestSecretEnv = "ARGENT_REQUEST_SECRET"

func requestSecretEnabled(env func(string) string) bool {
	if env == nil {
		return true
	}
	return strings.TrimSpace(env(requestSecretEnv)) != "0"
}

// textSecretRequester 是 CLI 终端上的最小实现。TUI 可在收尾时用模态弹窗替换
// 它（wireRequestSecretWith 接受任意 SecretRequester），无需改动工具实现。
type textSecretRequester struct {
	in  io.Reader
	out io.Writer
}

func (a *textSecretRequester) RequestSecret(ctx context.Context, name, reason string) (string, error) {
	if a == nil || a.in == nil || a.out == nil {
		return "", fmt.Errorf("no interactive input/output channel")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprintf(a.out, "\n[request_secret] %s — %s\n", name, reason)
	fmt.Fprintf(a.out, "输入值（仅写入凭据存储并作为 $%s 暴露给子进程，不进入会话/事件）: ", name)
	line, err := readAskLine(a.in)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", fmt.Errorf("empty value for %s", name)
	}
	return value, nil
}

// authSecretSink 复用 auth.Store（Windows 上 DPAPI 加密、0600 文件）保存值。
// 保存成功后用 os.Setenv 把它暴露为 $NAME：bash 工具的内建 shell 从 os.Environ
// 构造变量表，硬化路径经 proc.SanitizeEnv 继承进程环境，因此子进程可以读到它。
// 除内存中的环境变量外，值从不写入会话目录。
type authSecretSink struct {
	store *auth.Store
}

func (s *authSecretSink) SaveSecret(name, value string) error {
	if err := s.store.Put(auth.Token{
		Provider: "secret:" + name,
		Kind:     auth.KindAPIKey,
		Access:   value,
	}); err != nil {
		return err
	}
	return os.Setenv(name, value)
}

func (s *authSecretSink) HasSecret(name string) bool {
	_, ok := s.store.Get("secret:" + name)
	return ok
}

// wireRequestSecret 在安装交互通道时给本次运行的每个工具注册表追加 request_secret。
// 非交互传输（--rpc/headless）没有收集值的通道，此时零副作用：不注册工具、不读 stdin。
// storage 为空或凭据目录不可用时同样静默跳过，不影响既有运行。
func wireRequestSecret(stdio cli.IO, set *toolset, storage string) {
	if !requestSecretEnabled(stdio.Env) {
		return
	}
	wireRequestSecretWith(stdio, set, storage, nil)
}

// wireRequestSecretWith 允许 TUI 在收尾时注入模态 requester；传 nil 时回落到
// 终端文本实现（仍需 TTY）。它是 wireRequestSecret 的唯一实现入口。
func wireRequestSecretWith(stdio cli.IO, set *toolset, storage string, requester tools.SecretRequester) {
	if set == nil || set.base == nil || strings.TrimSpace(storage) == "" {
		return
	}
	if requester == nil {
		if stdio.In == nil || stdio.Out == nil || stdio.HasTerminal == nil || !stdio.HasTerminal() {
			return
		}
		requester = &textSecretRequester{in: stdio.In, out: stdio.Out}
	}
	store, err := auth.Open(filepath.Join(storage, "auth"))
	if err != nil {
		return
	}
	sink := &authSecretSink{store: store}
	for _, reg := range set.base.regs {
		tools.RegisterRequestSecret(reg, requester, sink)
	}
}
