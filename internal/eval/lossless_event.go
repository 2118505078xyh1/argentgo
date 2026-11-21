package eval

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// 本文件是“无损事件镜像”的独立原型，用于给第 11 轮 app 改造提供参考实现。
//
// 背景：app.SurfaceEvent 是 8 字段 DTO，无法承载 agent.Event 的 25 个字段，
// 直接接线会破坏 SSE/RPC 的字节。但 app 不能 import eval，eval 也不应 import agent
// （eval 是场景运行器）。因此这里在 eval 内做一个独立的无损镜像，重点验证
// “字段奇偶 + 字节稳定”的方法论，而不是真正替换传输层。
//
// eval 不 import agent，所以 5 个嵌套字段（Usage/TotalUsage/Failure/Checkpoint/Retry）
// 用下面的等价类型承载，它们的字段名、json tag、omitempty 和声明顺序与 agent 侧完全一致。
// 接线到 app 时，应把这些字段改为直接复用 agent 侧的嵌套类型
// （llm.Usage / llm.FailureInfo / agent.Checkpoint / agent.RetryInfo），
// 而不是保留这些镜像类型。

// losslessEventKind 镜像 agent.EventKind（命名 string 类型），保证仍编码为 JSON 字符串。
type losslessEventKind string

// losslessUsage 镜像 llm.Usage 的 JSON 形态。
type losslessUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// losslessFailureInfo 镜像 llm.FailureInfo 的 JSON 形态。
// llm.FailureInfo 的字段没有 json tag，故 JSON 键沿用 Go 字段名（Kind/Retryable/UserAction/ErrorID）。
type losslessFailureInfo struct {
	Kind       string
	Retryable  bool
	UserAction string
	ErrorID    string
}

// losslessCheckpoint / losslessCheckpointCall 镜像 agent.Checkpoint / agent.CheckpointCall。
type losslessCheckpoint struct {
	Turn  int                      `json:"turn"`
	Calls []losslessCheckpointCall `json:"calls"`
	Paths []string                 `json:"paths"`
}

type losslessCheckpointCall struct {
	ID   string `json:"id,omitempty"`
	Tool string `json:"tool"`
	Args string `json:"args,omitempty"`
}

// losslessRetryInfo 镜像 agent.RetryInfo 的 JSON 形态。
type losslessRetryInfo struct {
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"max_attempts,omitempty"`
	DelayMS     int64  `json:"delay_ms,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Model       string `json:"model,omitempty"`
}

// LosslessEvent 逐字段镜像 agent.Event（见 internal/agent/events.go:84-139）。
// 字段名、类型、json tag、omitempty 与声明顺序逐项一致；仅 5 个嵌套字段用本包内的
// 等价类型承载（见文件头说明）。新增字段必须同步更新 parity 测试中的期望清单。
type LosslessEvent struct {
	Kind       losslessEventKind
	Turn       int
	Depth      int
	Text       string
	ToolCallID string
	ToolName   string
	Args       string
	// Intent 是模型附加到工具调用上的简洁 `i` 字符串。
	Intent     string
	Output     string
	IsError    bool
	DurationMS int64
	Usage      losslessUsage
	TotalUsage losslessUsage
	Err        string
	// Failure 是 Err 经过脱敏后的分类，只在 error 事件上填充；其他种类保持 nil，
	// 以便其 JSON 逐字节不变。
	Failure         *losslessFailureInfo `json:"failure,omitempty"`
	Reason          string
	PendingMessages int
	Checkpoint      *losslessCheckpoint
	Retry           *losslessRetryInfo `json:"retry,omitempty"`
	// StartedAtMS / FinishedAtMS 是绝对墙上时钟；零值表示“无计时”，在 JSON 中省略。
	StartedAtMS  int64 `json:"started_at_ms,omitempty"`
	FinishedAtMS int64 `json:"finished_at_ms,omitempty"`

	// 以下字段只在 partial 事件上填充，全部带 omitempty，保证既有事件字节不变。
	Partial           bool   `json:"partial,omitempty"`
	PartialStopReason string `json:"partial_stop_reason,omitempty"`
	PartialModel      string `json:"partial_model,omitempty"`
	PartialTruncated  bool   `json:"partial_truncated,omitempty"`
}

// MirrorEvent 通过反射按字段名把任意事件结构投影到 LosslessEvent。
// 源结构中不存在的字段保持零值；嵌套结构按字段名递归投影，从而在 eval 内用等价类型
// 也能得到与 agent.Event 逐字节相同的 JSON。
//
// 它是纯函数：只读入参，不修改它。传入 nil、非结构或多层指针均可安全处理。
func MirrorEvent(e any) LosslessEvent {
	var out LosslessEvent
	src := reflect.ValueOf(e)
	for src.Kind() == reflect.Pointer {
		if src.IsNil() {
			return out
		}
		src = src.Elem()
	}
	if src.Kind() != reflect.Struct {
		return out
	}
	dst := reflect.ValueOf(&out).Elem()
	projectFields(src, dst)
	return out
}

// projectFields 按目标结构的字段名在源结构中查找同名字段并投影。
func projectFields(src, dst reflect.Value) {
	dt := dst.Type()
	for i := 0; i < dt.NumField(); i++ {
		sf := src.FieldByName(dt.Field(i).Name)
		if !sf.IsValid() {
			// Key 不存在：目标字段保持零值。
			continue
		}
		projectValue(sf, dst.Field(i))
	}
}

// projectValue 把一个源字段投影到目标字段，处理 nil 指针、指针、结构、切片以及可转换的基本类型。
func projectValue(s, d reflect.Value) {
	if !s.IsValid() || !d.CanSet() {
		return
	}
	// 源为 nil 指针时目标保持 nil，否则 omitempty 会失效（编码出 null 而非省略）。
	if s.Kind() == reflect.Pointer && s.IsNil() {
		return
	}
	// 指针：分配目标并投影被指向的值。
	if s.Kind() == reflect.Pointer && d.Kind() == reflect.Pointer {
		d.Set(reflect.New(d.Type().Elem()))
		projectValue(s.Elem(), d.Elem())
		return
	}
	// 结构：按字段名递归投影。
	if s.Kind() == reflect.Struct && d.Kind() == reflect.Struct {
		projectFields(s, d)
		return
	}
	// 切片：逐元素投影；nil 切片保持 nil。
	if s.Kind() == reflect.Slice && d.Kind() == reflect.Slice {
		if s.IsNil() {
			return
		}
		out := reflect.MakeSlice(d.Type(), s.Len(), s.Len())
		for i := 0; i < s.Len(); i++ {
			projectValue(s.Index(i), out.Index(i))
		}
		d.Set(out)
		return
	}
	// 类型相同：直接赋值。
	if s.Type().AssignableTo(d.Type()) {
		d.Set(s)
		return
	}
	// 底层类型相同（如不同的命名 string/int 类型）：转换后赋值。
	if s.Type().ConvertibleTo(d.Type()) {
		d.Set(s.Convert(d.Type()))
	}
}

// JSON 以稳定字节序列化事件：关闭 HTML 转义（避免 <、>、& 被改写），并去掉
// encoding/json 的 Encoder 追加的尾部换行，便于直接作为 SSE/RPC 的一行载荷。
func (e LosslessEvent) JSON() []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		// LosslessEvent 的字段在编译期即可确定，正常不会编码失败；
		// 万一失败也不返回半截字节。
		return nil
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
