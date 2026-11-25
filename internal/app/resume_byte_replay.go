package app

import (
	"strings"

	"example.com/argentgo/internal/session"
)

// 本文件实现 resume 的字节级回放（killi 决策 21）：当上次进程在"请求已发出、
// 回复未落盘"时崩溃（RecoveryInterruptedPrompt），且本会话开启了请求快照，
// 就把最后一条快照的原始请求体原样重发给提供方，保持缓存前缀命中。
// 严格 opt-in（ARGENT_RESUME_BYTE_REPLAY=1 且 ARGENT_REQUEST_SNAPSHOT=1），
// 任何校验失败都静默降级为零值，回退到逐字节不变的语义重建路径。

const resumeByteReplayEnableEnv = "ARGENT_RESUME_BYTE_REPLAY"

// resumeByteReplayEnabled 报告开关是否开启（真值，忽略大小写与首尾空白）。
func resumeByteReplayEnabled(env func(string) string) bool {
	if env == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(env(resumeByteReplayEnableEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// replayCandidate 是一次可回放的快照候选（协议/模型/字节）。
type replayCandidate struct {
	body     []byte
	model    string
	protocol string
}

// shouldReplayCategory 报告该恢复类别是否允许字节回放。
// 只有"中断的提示"（请求已发出但回复未落盘）才需要重发原始字节；
// 干净会话、降级历史、中断的轮次都不回放。
func shouldReplayCategory(category string) bool {
	return category == string(session.RecoveryInterruptedPrompt)
}

// selectReplayCandidate 从会话快照中挑选可用于回放的候选，并做全部校验：
//   - 快照必须有效、位于活动路径、其后无 assistant 回复、无压缩；
//   - 快照的协议与模型必须与本次运行一致（防止跨协议/跨模型回放）；
//
// 任一条件不满足返回 nil（调用方回退普通 Chat）。
func selectReplayCandidate(snapshot *session.ReplaySnapshot, category, model, protocol string) *replayCandidate {
	if snapshot == nil || !shouldReplayCategory(category) {
		return nil
	}
	if snapshot.HasLaterReply || snapshot.HasCompaction {
		return nil
	}
	p := snapshot.Payload
	if p.Protocol != protocol || p.Model != model {
		return nil
	}
	return &replayCandidate{body: p.Body, model: p.Model, protocol: p.Protocol}
}

// replayFromSession 是一次完整接线：在开关开启且快照开启时读取会话快照并校验。
func replayFromSession(sess *session.Session, env func(string) string, category, model, protocol string) *replayCandidate {
	if !resumeByteReplayEnabled(env) || !requestSnapshotEnabled(env) || sess == nil {
		return nil
	}
	return selectReplayCandidate(sess.ReplaySnapshotOf(), category, model, protocol)
}

// 以下三个 nil-safe 访问器供 Runner 构造使用：候选为 nil 时返回零值，
// agent 侧 ReplayBody 为空即关闭回放，请求走逐字节不变的普通 Chat。
func replayBodyOf(c *replayCandidate) []byte {
	if c == nil {
		return nil
	}
	return c.body
}

func replayModelOf(c *replayCandidate) string {
	if c == nil {
		return ""
	}
	return c.model
}

func replayProtocolOf(c *replayCandidate) string {
	if c == nil {
		return ""
	}
	return c.protocol
}
