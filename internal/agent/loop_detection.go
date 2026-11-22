package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
)

// 循环检测的进程级 opt-in 开关。默认关闭：不改动既有运行循环的任何行为，
// 与历史会话记录逐字节一致。检测在 loop.go 的每个工具批次后调用；开关由
// 装配层 internal/app 通过 ARGENT_LOOP_DETECTION 接线（见 loop_detection_wire.go）。
var loopDetection atomic.Bool

// SetLoopDetection 打开或关闭工具调用循环检测。
// 关闭（默认）时引擎不会调用 DetectLoop，行为与历史运行一致。
func SetLoopDetection(enabled bool) {
	loopDetection.Store(enabled)
}

// LoopDetectionEnabled 报告当前是否启用了工具调用循环检测。
// 它是热路径（loop.go 的批次后检查）与装配层诊断共用的只读入口。
func LoopDetectionEnabled() bool {
	return loopDetection.Load()
}

// 参数钳制默认值：仅当调用方传入非正数时生效。
const (
	defaultLoopWindow    = 10
	defaultLoopThreshold = 5
)

// ToolStep 描述一步工具调用的归一化输入：工具名、输入与配对的输出。
// DetectLoop 对这三者做签名，同名但输出不同的两步不会得到相同签名。
type ToolStep struct {
	Name   string
	Input  string
	Output string
}

// stepSignature 返回一步工具调用的 SHA-256 十六进制签名。
// 以 \x00 作为分隔符拼接三段，避免相邻字段内容粘连产生歧义；SHA-256 对
// 任意字节（含 CJK 多字节字符与 emoji）逐字节计算，结果稳定且与语言无关。
func stepSignature(step ToolStep) string {
	sum := sha256.Sum256([]byte(step.Name + "\x00" + step.Input + "\x00" + step.Output))
	return hex.EncodeToString(sum[:])
}

// DetectLoop 在最近 window 步内按签名统计重复，若任一签名出现次数超过
// threshold 则判定为卡死，返回该签名、出现次数与 true。
//
// 语义要点：
//   - 只看最近 window 步（不足 window 时看全部）；window<=0 取默认值 10。
//   - threshold<=0 取默认值 5；判定条件是 count > threshold（严格超过）。
//   - 签名由 name\x00input\x00output 的 SHA-256 得到，比纯文本重复更鲁棒：
//     同名但输入/输出不同的调用不会误判为循环。
//   - 并列时返回计数最高、且在窗口内最早出现的签名，保证确定性。
//
// 本函数是纯函数：不读写全局开关、不修改入参、不读时钟、不做 I/O，
// 因此对同样的入参恒定返回同样的结果，且可安全地被任意并发调用。
func DetectLoop(steps []ToolStep, window, threshold int) (signature string, count int, detected bool) {
	if window <= 0 {
		window = defaultLoopWindow
	}
	if threshold <= 0 {
		threshold = defaultLoopThreshold
	}

	// 仅统计窗口内的尾部步骤；不裁剪原切片，避免改动入参。
	start := 0
	if len(steps) > window {
		start = len(steps) - window
	}

	counts := make(map[string]int, window)
	firstSeen := make(map[string]int, window)
	for i := start; i < len(steps); i++ {
		sig := stepSignature(steps[i])
		if _, ok := counts[sig]; !ok {
			firstSeen[sig] = i
		}
		counts[sig]++
	}

	// 选超过阈值且计数最高的签名；并列取窗口内最早出现者，保证确定性。
	var bestSig string
	bestCount, bestFirst := 0, 0
	for sig, n := range counts {
		if n <= threshold {
			continue
		}
		first := firstSeen[sig]
		if n > bestCount || (n == bestCount && first < bestFirst) {
			bestSig, bestCount, bestFirst = sig, n, first
		}
	}
	if bestSig == "" {
		return "", 0, false
	}
	return bestSig, bestCount, true
}
