package bash

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 本文件实现软超时观察语义：长命令在 yield 窗口内未结束时，工具结果
// 携带结构化的 still_running/cwd/pid/exit_code（从 PS1 哨兵解析、与
// stdout 分离），而不是让模型误以为进程已死、只能重跑造成重复副作用。

// obsMarker 是 PS1 哨兵前缀。探针在会话命令结束后打印一行
// `__ARGENT_META__|<cwd>|<pid>|<exit_code>`，采用尾锚定切分，命令先前
// 输出的伪造哨兵不会干扰真正的探针。
const obsMarker = "__ARGENT_META__|"

// bashObservation 是一次软超时观察的结构化结果。
// StillRunning 为真时 ExitCode 取 -1，专表「仍在运行（软超时）」，
// 与 exitCodeFromWait 中「被信号终止」的 -1 语义刻意区分：观察侧的 -1
// 只用于告诉模型进程还活着，不能据此断定它已失败。
type bashObservation struct {
	StillRunning bool
	CWD          string
	PID          int
	ExitCode     int
}

// metaProbeCommand 构造追加在会话命令末尾的 PS1 哨兵探针。
// 先捕获上一条命令的退出码，再打印 cwd / shell pid / 退出码。
// 与持久化 shell 的 probeCommand 同构但使用独立前缀，两条路径互不干扰。
func metaProbeCommand() string {
	return "__meta_rc=$?; printf '" + obsMarker + "%s|%s|%d\\n' \"$PWD\" \"$$\" \"$__meta_rc\""
}

// splitMeta 从原始输出中切出 PS1 哨兵并解析元数据，返回去掉哨兵行的
// stdout。缺少完整哨兵行（被截断/进程仍运行）时原样返回 stdout 并给出
// ok=false，调用方据此保留上一轮记住的 cwd/pid。
func splitMeta(out string) (stdout, cwd string, pid, code int, ok bool) {
	idx := strings.LastIndex(out, obsMarker)
	if idx < 0 {
		return out, "", 0, 0, false
	}
	rest := out[idx+len(obsMarker):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return out, "", 0, 0, false
	}
	line := strings.TrimSuffix(rest[:nl], "\r") // Windows CRLF
	parts := strings.Split(line, "|")
	if len(parts) != 3 {
		return out, "", 0, 0, false
	}
	cwd = parts[0]
	pid, e1 := strconv.Atoi(parts[1])
	code, e2 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil {
		return out, "", 0, 0, false
	}
	lineStart := idx
	for lineStart > 0 && out[lineStart-1] != '\n' {
		lineStart--
	}
	return out[:lineStart], cwd, pid, code, true
}

// sessionMeta 是会话在两次工具调用之间需要记住的最小元数据。
type sessionMeta struct {
	PID int
	CWD string
}

var (
	sessionMetaMu sync.Mutex
	sessionMetas  = make(map[string]sessionMeta)
)

func rememberSessionMeta(id string, m sessionMeta) {
	sessionMetaMu.Lock()
	sessionMetas[id] = m
	sessionMetaMu.Unlock()
}

func lookupSessionMeta(id string) (sessionMeta, bool) {
	sessionMetaMu.Lock()
	m, ok := sessionMetas[id]
	sessionMetaMu.Unlock()
	return m, ok
}

func forgetSessionMeta(id string) {
	sessionMetaMu.Lock()
	delete(sessionMetas, id)
	sessionMetaMu.Unlock()
}

// observationFromOutput 把一次会话读取的原始输出整理为 (stdout, observation,
// sentinelOK)。known 是该会话此前记住的 pid/cwd；哨兵出现时以哨兵值覆盖。
// 仍在运行时退出码固定 -1；已退出时取哨兵退出码（无哨兵则退化为 0）。
func observationFromOutput(out string, running bool, known sessionMeta) (string, bashObservation, bool) {
	stdout, cwd, pid, code, ok := splitMeta(out)
	obs := bashObservation{
		StillRunning: running,
		CWD:          known.CWD,
		PID:          known.PID,
		ExitCode:     -1,
	}
	if ok {
		obs.CWD = cwd
		obs.PID = pid
	}
	if !running {
		obs.ExitCode = 0
		if ok {
			obs.ExitCode = code
		}
	}
	return stdout, obs, ok
}

// observationLine 把结构化元数据渲染为单独一行，放在 stdout 之前，
// 从而与命令正文分离。exit_code=-1 专表软超时仍在运行。
func observationLine(obs bashObservation) string {
	return fmt.Sprintf("[observation: still_running=%t, cwd=%q, pid=%d, exit_code=%d]\n",
		obs.StillRunning, obs.CWD, obs.PID, obs.ExitCode)
}

// formatBashObservation 把结构化观察行与清洗裁剪后的 stdout 拼成工具结果：
// 观察行始终在最前且独立成行，stdout 走既有清洗/裁剪管线，空输出回落
// bashNoOutput，保持与普通 bash 路径的可观察格式一致。
func formatBashObservation(ctx context.Context, stdout string, obs bashObservation) string {
	text := clipBashOutput(ctx, stdout)
	head := observationLine(obs)
	if strings.TrimSpace(text) == "" {
		return head + bashNoOutput(stdout, text)
	}
	return head + text
}

// interruptYield 限制中断写等待窗口，避免 yield 缺省（0→注册表默认
// 10s）让一次 C-c 挂住整轮：取 (0,250ms] 区间内的值。
func interruptYield(yield time.Duration) time.Duration {
	if yield <= 0 || yield > 250*time.Millisecond {
		return 250 * time.Millisecond
	}
	return yield
}
