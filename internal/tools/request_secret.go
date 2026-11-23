package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// 本文件实现 request_secret 工具（TODO #18，学自 OpenHands）：模型在需要临时凭据时
// 只提交 name + reason，值由交互层收集并落到凭据存储。工具本身从不接收值作为
// 参数，回灌给模型的也只有 [secret-request] 状态前缀消息，绝不携带密钥字节。
//
// 值经 SecretSink 写入凭据存储并暴露为同名进程环境变量，因此 bash 子进程只能用
// $NAME 读取；它不进入工具结果、事件流、模型消息或会话 JSONL。

const (
	requestSecretName           = "request_secret"
	requestSecretMaxNameRunes   = 64
	requestSecretMaxReasonRunes = 500
)

// SecretRequester 由 app 注入，负责在交互通道上收集密钥值。
// 接口定义在消费方（tools），app 提供 CLI 文本或 TUI 模态弹窗实现。
type SecretRequester interface {
	RequestSecret(ctx context.Context, name, reason string) (string, error)
}

// SecretSink 由 app 注入，负责把值写入既有凭据存储，并把它暴露为同名环境变量
// （供 bash 子进程以 $NAME 读取）。实现必须保证值本身不进入任何会话/事件记录。
type SecretSink interface {
	SaveSecret(name, value string) error
}

// SecretSinkLookup 是可选的查询端：已保存过的名字不再重复询问用户。
// 未实现该接口的 sink 只依赖本工具实例内的 seen 去重。
type SecretSinkLookup interface {
	HasSecret(name string) bool
}

type requestSecretTool struct {
	mu        sync.Mutex
	requester SecretRequester
	sink      SecretSink
	seen      map[string]bool
}

func (*requestSecretTool) Name() string { return requestSecretName }

func (*requestSecretTool) Mutating() bool {
	// 写的是用户凭据存储而非工作区文件；它不经过工作区审批链，也不改动仓库。
	return false
}

func (*requestSecretTool) Description() string {
	return "Request a secret (API key, token, password) from the user when a task needs one. " +
		"Pass only a stable env-style name and a short reason. NEVER put a secret value in this " +
		"tool's arguments, and NEVER ask the user to paste a secret into the chat. " +
		"After saving, the value is available to bash commands only as $NAME. " +
		"Each missing name is asked for at most once."
}

func (*requestSecretTool) Schema() string {
	return `{"type":"object","properties":{"name":{"type":"string","description":"uppercase env-style name, e.g. GITHUB_TOKEN"},"reason":{"type":"string","description":"short reason shown to the user; never include a secret value"}},"required":["name","reason"]}`
}

func (t *requestSecretTool) Run(ctx context.Context, root string, raw json.RawMessage) (string, error) {
	if t == nil || t.requester == nil || t.sink == nil {
		return "", fmt.Errorf("request_secret: no interactive credential channel is wired in this session")
	}
	var args struct {
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("request_secret: bad arguments: %w", err)
	}
	name, err := normalizeSecretName(args.Name)
	if err != nil {
		return "", err
	}
	reason := strings.TrimSpace(args.Reason)
	if reason == "" {
		return "", fmt.Errorf("request_secret: reason must not be empty")
	}
	if utf8.RuneCountInString(reason) > requestSecretMaxReasonRunes {
		return "", fmt.Errorf("request_secret: reason must be at most %d characters", requestSecretMaxReasonRunes)
	}

	// 一处缺失只问一次：本次运行内用 seen 去重；跨运行/恢复时由 sink 的查询端兜底。
	t.mu.Lock()
	if t.seen == nil {
		t.seen = make(map[string]bool)
	}
	already := t.seen[name]
	t.mu.Unlock()
	if !already {
		if lookup, ok := t.sink.(SecretSinkLookup); ok && lookup.HasSecret(name) {
			already = true
		}
	}

	if !already {
		value, err := t.requester.RequestSecret(ctx, name, reason)
		if err != nil {
			return "", fmt.Errorf("request_secret: %w", err)
		}
		if value == "" {
			return "", fmt.Errorf("request_secret: empty value for %s", name)
		}
		if err := t.sink.SaveSecret(name, value); err != nil {
			return "", fmt.Errorf("request_secret: save %s: %w", name, err)
		}
		t.mu.Lock()
		t.seen[name] = true
		t.mu.Unlock()
	}

	// 唯一回灌给模型的载荷：状态 + 名字。密钥字节绝不出现在这里。
	return `[secret-request] {"status":"saved","name":"` + name + `"}`, nil
}

// normalizeSecretName 把名字收敛为安全的大写环境变量标识符。拒绝任何可能破坏
// 环境变量语义的字形（等号、空白、控制字符、引号等），并挡住会覆写关键变量的名称。
func normalizeSecretName(raw string) (string, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if name == "" || utf8.RuneCountInString(name) > requestSecretMaxNameRunes {
		return "", fmt.Errorf("request_secret: name must be 1-%d characters", requestSecretMaxNameRunes)
	}
	for i, r := range name {
		ok := r == '_' || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return "", fmt.Errorf("request_secret: name must be an env-style identifier (letters, digits, underscore)")
		}
	}
	switch name {
	case "PATH", "HOME", "TEMP", "TMP", "USERPROFILE", "COMSPEC", "SYSTEMROOT", "PWD", "SHELL":
		return "", fmt.Errorf("request_secret: %s is a reserved environment variable", name)
	}
	return name, nil
}

// RegisterRequestSecret 把 request_secret 显式追加到 r 的末尾。幂等；nil 是空操作。
// 与 ask_user 一样，它由 app 在装配面显式注册，不进 New/NewWithLimit 的默认清单。
func RegisterRequestSecret(r *Registry, requester SecretRequester, sink SecretSink) {
	if r == nil {
		return
	}
	if _, exists := r.tools[requestSecretName]; exists {
		return
	}
	r.tools[requestSecretName] = &requestSecretTool{requester: requester, sink: sink}
	r.order = append(r.order, requestSecretName)
}
