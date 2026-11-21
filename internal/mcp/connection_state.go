package mcp

import (
	"errors"
	"sync"
)

// ConnState 表示连接生命周期中的一个离散状态。底层类型为 string，常量取值
// 便于日志与诊断输出；但转换合法性只由 connTransitions 矩阵决定，不依赖字符串
// 文本内容，因此与语言、大小写无关。
type ConnState string

// 连接状态的取值集合。
const (
	ConnDisconnected ConnState = "disconnected" // 初始态：尚未发起连接
	ConnConnecting   ConnState = "connecting"   // 正在建立连接
	ConnReady        ConnState = "ready"        // 连接就绪，可正常收发
	ConnFailed       ConnState = "failed"       // 连接失败，可重试
	ConnClosed       ConnState = "closed"       // 终态：连接已关闭，不可再转换
)

// ErrInvalidTransition 表示目标状态不在当前状态的合法转换矩阵中，或目标状态
// 根本不是已知状态。调用方可用 errors.Is 稳定匹配，无需读取错误文本。
var ErrInvalidTransition = errors.New("mcp: invalid connection state transition")

// ErrStaleGeneration 表示调用方传入的生成号与当前生成号不符，即其观测已过期。
// 该哨兵让迟到的异步事件（例如旧连接尝试的 ready）无法污染较新的连接状态。
var ErrStaleGeneration = errors.New("mcp: stale connection generation")

// connTransitions 是唯一的合法性来源：仅列出允许的出边。closed 无出边，是终态。
// 任何未知状态（包括零值以外的非法字符串）都不在表中，天然被判为非法。
var connTransitions = map[ConnState]map[ConnState]bool{
	ConnDisconnected: {ConnConnecting: true, ConnClosed: true},
	ConnConnecting:   {ConnReady: true, ConnFailed: true, ConnClosed: true},
	ConnFailed:       {ConnConnecting: true, ConnClosed: true},
	ConnReady:        {ConnFailed: true, ConnClosed: true},
	ConnClosed:       {},
}

// ConnFsm 是一个纯内存的连接状态机，带锁保存当前状态与单调递增的生成号。
// 零值可直接使用：空状态被视为 disconnected，生成号为 0。它不做任何 I/O，
// 也不接入现有连接流程；调用方按需调用 Transition 推进状态。
//
// mu 保护 state 与 gen 两个字段，所有读写均在锁下进行。
type ConnFsm struct {
	mu    sync.Mutex
	state ConnState
	gen   uint64
}

// State 返回当前状态与当前生成号的快照。
// 零值 FSM 的状态报告为 disconnected、生成号为 0。
func (f *ConnFsm) State() (ConnState, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.effectiveStateLocked(), f.gen
}

// Transition 尝试把状态从当前值推进到 to。
//
// 语义如下：
//   - gen 是调用方最近一次通过 State 观测到的生成号。只有它与 FSM 当前生成号
//     相等，转换才会被接受；否则返回 ErrStaleGeneration，状态与生成号都不变。
//     这让基于过期观测的迟到事件无法破坏较新的连接状态。
//   - 若 to 不是当前状态的合法出边（含未知状态），返回 ErrInvalidTransition，
//     状态与生成号同样不变。
//   - 合法转换会把状态置为 to，并把生成号加一（单调递增）。
//
// 生成号的校验先于合法性校验：对一个过期的观测，即使目标状态本身非法，也优先
// 报告 ErrStaleGeneration，因为调用方首先应当刷新观测再决定下一步动作。
func (f *ConnFsm) Transition(to ConnState, gen uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if gen != f.gen {
		return ErrStaleGeneration
	}

	from := f.effectiveStateLocked()
	if !connTransitions[from][to] {
		return ErrInvalidTransition
	}

	f.state = to
	f.gen++
	return nil
}

// effectiveStateLocked 把零值的空状态归一化为 disconnected。调用方须持有 f.mu。
func (f *ConnFsm) effectiveStateLocked() ConnState {
	if f.state == "" {
		return ConnDisconnected
	}
	return f.state
}
