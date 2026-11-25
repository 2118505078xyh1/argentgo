package app

import (
	"bytes"
	"encoding/json"

	"example.com/argentgo/internal/agent"
)

// 本文件提供一个"统一事件 DTO 投影"原语：把 agent 的事件流投影成一份与传输无关的
// 公共形状，使 SSE / RPC / ACP 等表面日后可以共用同一份序列化结果。
//
// 默认零调用方：这里既不注册路由、也不发送任何事件，只定义数据结构与纯函数。
// 现有事件的发送路径完全不受影响。

// SurfaceEvent 是跨表面的统一事件 DTO。它只保留各表面都能理解的公共字段，
// 其余信息由调用方按需放入 Extra。所有字段都带 omitempty 语义：零值字段在
// JSON 中被省略，因此 SurfaceEvent{} 序列化为 {}。
type SurfaceEvent struct {
	Kind       string            `json:"kind,omitempty"`
	Seq        uint64            `json:"seq,omitempty"`
	AtMS       int64             `json:"at_ms,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Text       string            `json:"text,omitempty"`
	IsError    bool              `json:"is_error,omitempty"`
	Depth      int               `json:"depth,omitempty"`
	Extra      map[string]string `json:"extra,omitempty"`
}

// FromAgentEvent 把一条 agent.Event 投影成统一 DTO。
//
// 逐字段复制：Kind 转为字符串，ToolCallID / Text / IsError / Depth 直接取值；
// Seq 由调用方提供，用于跨表面的单调序号。时间戳 AtMS 优先取 StartedAtMS，
// 为 0 时回退到 FinishedAtMS，从而让只有完成时间的事件也带有可用的时标。
//
// Extra 被初始化为一份新的空映射（独立副本）：调用方可以安全地写入自己的键值，
// 而不必担心与任何其他事件共享状态；由于映射为空，它仍会被 omitempty 省略。
//
// ev 以值传递，函数不会修改它，也不会持有其指针字段（Failure / Retry / Checkpoint）。
func FromAgentEvent(ev agent.Event, seq uint64) SurfaceEvent {
	atMS := ev.StartedAtMS
	if atMS == 0 {
		atMS = ev.FinishedAtMS
	}
	return SurfaceEvent{
		Kind:       string(ev.Kind),
		Seq:        seq,
		AtMS:       atMS,
		ToolCallID: ev.ToolCallID,
		Text:       ev.Text,
		IsError:    ev.IsError,
		Depth:      ev.Depth,
		Extra:      make(map[string]string),
	}
}

// JSON 把 DTO 序列化为稳定、可跨表面复用的 JSON 字节。稳定性体现在：
//
//   - 字段顺序固定：按 SurfaceEvent 的声明顺序写出；
//   - 键排序：encoding/json 对映射键按字典序排序，输出与插入顺序无关；
//   - CJK 安全：SetEscapeHTML(false) 让中文与 <、>、& 按 UTF-8 原样输出，
//     不产生 \uXXXX 转义；
//   - 去尾换行：json.Encoder 追加的末尾 \n 被去掉。
//
// 序列化前会复制 Extra，因此该方法不修改接收者，也不修改调用方持有的映射；
// 同一 DTO 总是返回逐字节一致的切片。zero 值返回 {}。
func (e SurfaceEvent) JSON() []byte {
	out := e
	if len(e.Extra) > 0 {
		copied := make(map[string]string, len(e.Extra))
		for key, value := range e.Extra {
			copied[key] = value
		}
		out.Extra = copied
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// SurfaceEvent 全由基本类型与 map[string]string 组成，Encode 不会失败。
	_ = enc.Encode(out)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// SameKind 报告两个 DTO 是否属于同一种事件。它只比较 Kind，
// 与序号、时间戳、负载等字段无关。
func SameKind(a, b SurfaceEvent) bool {
	return a.Kind == b.Kind
}
