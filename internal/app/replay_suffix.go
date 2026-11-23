package app

import (
	"errors"
	"strconv"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// replay_suffix.go 提供会话重放恢复公告的纯函数层。
//
// 这里把 session.Replay 得到的状态投影成一段可拼到恢复公告末尾的中文诊断
// 后缀，外加一个默认开启的 env 开关（=0 紧急关闭）。stdlib-only、零 I/O、不调用
// 模型、不改 NeedsConfirmation，也绝不把合成的中止结果当作真实应答。纯函数层由
// Execute 的恢复路径接线（见 app_execute.go），关闭时零调用。

// replaySuffixIDRunes 是后缀中活跃叶子 id 的展示上限（按 rune 计）。
// 用 rune 而非字节截断，保证 CJK id 不会被切在 UTF-8 字符中间。
const replaySuffixIDRunes = 16

// replayRecoveryEnabled 报告重放恢复公告是否开启。默认开启，仅当
// ARGENT_RECOVERY_REPLAY=0（允许首尾空白）时紧急关闭。该开关已在
// cli/optin_metadata.go 注册（键 recovery_replay）。
func replayRecoveryEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env("ARGENT_RECOVERY_REPLAY")) != "0"
}

// boundedReplayID 返回用于展示的 id：不超过 replaySuffixIDRunes 时原样返回，
// 否则按 rune 截断并追加省略号。这样后缀长度有上界，且 CJK id 始终是合法 UTF-8。
func boundedReplayID(id string) string {
	runes := []rune(id)
	if len(runes) <= replaySuffixIDRunes {
		return id
	}
	return string(runes[:replaySuffixIDRunes]) + "…"
}

// replayNoticeSuffix 根据记录流返回一段确定性的中文诊断后缀。
//
// 它对 records 调用 session.Replay：完整流或空流返回空串，使现有公告字节保持
// 不变；否则返回形如 " (重放审计：N 个未完成的工具调用，完整=false，叶子=…)" 的
// 后缀——N 是未配对工具调用的个数，完整位原样镜像 ReplayState.Complete，叶子是
// 有界截断的活跃叶子 id。函数是纯函数：只读入参、不改 records 及其 Data，
// 同一入参逐字节稳定；它既不改变 NeedsConfirmation，也不把合成的中止结果
// 当作真实应答，仅描述重放审计发现的结构状态。
func replayNoticeSuffix(records []session.Record) string {
	st := session.Replay(records)
	if st.Complete || len(records) == 0 {
		return ""
	}
	return " (重放审计：" + strconv.Itoa(len(st.UnpairedToolCalls)) +
		" 个未完成的工具调用，完整=" + strconv.FormatBool(st.Complete) +
		"，叶子=" + boundedReplayID(st.ActiveLeaf) + ")"
}

// replayDiagnosticsSuffix 在 replayNoticeSuffix 的基础上，用 VerifyReplay
// 为 parent 环补一段显式提示，供 Execute 的恢复公告接线使用。
//
// 环探测采用自校验方式：expected 就是同一段 records 的 Replay 结果，因此除
// parent 环（replay 会先于逐字段比对返回 ErrCycle）外 VerifyReplay 恒为 nil；
// 这使 VerifyReplay 在这里充当一个纯粹的环探测器，而不引入任何"预期偏差"。
// 记录完整或为空时 replayNoticeSuffix 返回空串，本函数也随之返回空串，
// 历史公告逐字节不变。整个函数只做加法：既不改 NeedsConfirmation，也不把
// 合成的中止结果当作真实应答，仅描述重放审计发现的结构状态。
func replayDiagnosticsSuffix(records []session.Record) string {
	suffix := replayNoticeSuffix(records)
	if suffix == "" {
		return ""
	}
	if err := session.VerifyReplay(records, session.Replay(records)); errors.Is(err, session.ErrCycle) {
		suffix += "（检测到 parent 环）"
	}
	return suffix
}
