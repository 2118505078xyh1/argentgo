package app

import (
	"context"
	"strings"

	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/session"
)

// 本文件把 llm 的请求字节快照钩子接到会话记录。迁移自 killi 决策 21：
// 日志可以记录"实际发出的字节"，用于复现与审计；resume 字节回放是后续阶段，
// 本文件只做记录，不改任何请求构造或恢复路径。

const requestSnapshotEnableEnv = "ARGENT_REQUEST_SNAPSHOT"

// requestSnapshotEnabled 报告 opt-in 开关是否为真值。
func requestSnapshotEnabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env(requestSnapshotEnableEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// withRequestSnapshot 在 opt-in 开启且有会话时把快照收集器挂到 ctx。
// 默认（关闭或 sess 为 nil）时原样返回 ctx，llm 侧的钩子是零副作用 no-op。
//
// 注意：快照 body 含完整 system prompt 与工具结果，可能包含敏感文本；
// 它按 opt-in 写入会话文件，使用者应自行控制该文件的访问权限。
func withRequestSnapshot(ctx context.Context, env func(string) string, sess *session.Session) context.Context {
	if sess == nil || !requestSnapshotEnabled(env) {
		return ctx
	}
	return llm.WithRequestSnapshotSink(ctx, func(protocol, model string, body []byte) error {
		_, err := session.AppendRequestSnapshot(sess, session.RequestSnapshotPayload{
			Protocol: protocol,
			Model:    model,
			Body:     body,
		})
		return err
	})
}
