package cli

import (
	"encoding/json"
	"io"
	"sync"

	"example.com/argentgo/internal/llm"
)

// stderrTraceSink 把请求追踪摘要以单行 JSON 写到给定 writer（通常是 stderr）。
//
// 它实现 llm.TraceSink，供命令行层通过 llm.SetTraceSink 安装。每次 Record 在
// 互斥锁内完成 marshal 与单次 Write，保证并发调用下每条记录独占一行、不会与
// 其他记录交错成半行。写错误被有意忽略：追踪是尽力而为的诊断输出，不应影响
// 模型请求的正常流程。
type stderrTraceSink struct {
	mu sync.Mutex
	w  io.Writer
}

// newStderrTraceSink 构造一个写入 w 的收集器。w 为 nil 时所有 Record 都是安全
// no-op，便于测试或在不希望输出追踪时传入 nil。
func newStderrTraceSink(w io.Writer) *stderrTraceSink {
	return &stderrTraceSink{w: w}
}

// Record 把一条追踪记录序列化为一行 JSON 并写入底层 writer。
//
// encoding/json 原样保留 CJK 字符（只转义必要字符），配合 json.Marshal 永不计
// 入半个 UTF-8 序列；单次 Write 进一步保证并发调用时各行不交错。序列化或写入
// 失败时静默返回，不向上层传播。
func (s *stderrTraceSink) Record(t llm.RequestTrace) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.w == nil {
		return
	}
	line, err := json.Marshal(t)
	if err != nil {
		return
	}
	_, _ = s.w.Write(append(line, '\n'))
}
