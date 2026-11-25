package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"example.com/argentgo/internal/llm"
)

var (
	errWarmNoRaw  = errors.New("cache warm: chatter does not support raw replay")
	errWarmNoOnce = errors.New("cache warm: chatter does not support non-streaming completion")
)

// 本文件把 llm.CacheWarmer 接到长生命周期 runner 上。调度器本身不联网、不定时；
// 这里提供它要求的三件事：真实请求时刷新时钟、轮次边界报告空闲、以及需要保活时
// 重放最近一次真实请求的线协议字节（把生成上限压到 1 token）。
//
// 默认开启（改善体验），ARGENT_CACHE_WARM=0 可紧急关闭。重放只发往已经过审批的
// 同一已连接 chatter，不引入新的工具或权限面。

// warmReplayStore 在内存里保留最近一次真实请求的协议、模型与原始字节。它不落盘，
// 因此不触碰会话字节语义；进程退出后自然消失，下一次运行会重新采集。
type warmReplayStore struct {
	mu       sync.Mutex
	protocol string
	model    string
	body     []byte
}

func (s *warmReplayStore) record(protocol, model string, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.protocol = protocol
	s.model = model
	s.body = append([]byte(nil), body...)
	return nil
}

// snapshot 返回最近一次请求的副本，避免调用方修改内部字节。
func (s *warmReplayStore) snapshot() (protocol, model string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.protocol, s.model, append([]byte(nil), s.body...)
}

// warmRequestBody 把已采集请求的生成上限压到 1 token。改的是生成参数而非提示内容，
// 因此不影响提供方对提示前缀的缓存命中；没有上限字段的请求保持原样，避免引入
// 提供方无法识别的新字段。
func warmRequestBody(body []byte) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || len(obj) == 0 {
		return body
	}
	changed := false
	for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := obj[key]; ok {
			obj[key] = json.RawMessage("1")
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

// warmRecordChatter 在每次真实 Chat / ChatOnce 前刷新保活时钟，并透传所有可选接口。
// ChatRaw 刻意不刷新：它本身就是保活重放，不能把窗口不断往后推。
type warmRecordChatter struct {
	inner  llm.Chatter
	warmer *llm.CacheWarmer
}

func newWarmRecordChatter(inner llm.Chatter, warmer *llm.CacheWarmer) llm.Chatter {
	if inner == nil || warmer == nil {
		return inner
	}
	return &warmRecordChatter{inner: inner, warmer: warmer}
}

func (w *warmRecordChatter) Chat(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec, onText func(string)) (llm.Message, error) {
	w.warmer.RecordRequest(time.Now())
	return w.inner.Chat(ctx, model, messages, tools, onText)
}

func (w *warmRecordChatter) ChatRaw(ctx context.Context, model string, body []byte, onText func(string)) (llm.Message, error) {
	raw, ok := w.inner.(llm.RawChatter)
	if !ok {
		return llm.Message{}, errWarmNoRaw
	}
	return raw.ChatRaw(ctx, model, body, onText)
}

func (w *warmRecordChatter) ChatOnce(ctx context.Context, model string, messages []llm.Message, tools []llm.ToolSpec) (llm.Message, error) {
	once, ok := w.inner.(interface {
		ChatOnce(context.Context, string, []llm.Message, []llm.ToolSpec) (llm.Message, error)
	})
	if !ok {
		return llm.Message{}, errWarmNoOnce
	}
	w.warmer.RecordRequest(time.Now())
	return once.ChatOnce(ctx, model, messages, tools)
}

// warmSink context key：请求字节采集器随运行 ctx 传递，使 Execute 侧的 opt-in
// 会话快照与这里的内存 replay 共用同一条 hook 链，互不覆盖。
type warmSinkKey struct{}

type warmSink func(protocol, model string, body []byte) error

func withWarmSink(ctx context.Context, sink warmSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, warmSinkKey{}, sink)
}

func warmSinkFrom(ctx context.Context) warmSink {
	sink, _ := ctx.Value(warmSinkKey{}).(warmSink)
	return sink
}

// newRunnerWarmer 构造长驻运行器持有的保活调度器。TTL 取 30 分钟：短档缓存的实际
// 保留期通常在 5~30 分钟，过长会让保活错过窗口；超过 30 分钟的空闲由调度器自身的
// maxIdleWarmAge 拒绝，因此不会为一个几乎不会回来的会话持续付费。
func newRunnerWarmer(env func(string) string) (*llm.CacheWarmer, *warmReplayStore) {
	if !cacheWarmerEnabled(env) {
		return nil, nil
	}
	return &llm.CacheWarmer{Enabled: true, TTL: 30 * time.Minute}, &warmReplayStore{}
}

// maybeWarm 在轮次边界之后、新提示到来前决定是否发一次 1-token 保活重放。
// 失败被吞掉并交给调用方的诊断通道：保活是纯粹的优化，绝不能让一次本该成功的
// 用户提示因为保活请求失败而中断。
func maybeWarm(ctx context.Context, chatter llm.Chatter, warmer *llm.CacheWarmer, store *warmReplayStore) bool {
	if warmer == nil || store == nil || chatter == nil {
		return false
	}
	now := time.Now()
	if !warmer.ShouldWarm(now) {
		return false
	}
	_, model, body := store.snapshot()
	if len(body) == 0 || model == "" {
		return false
	}
	raw, ok := chatter.(llm.RawChatter)
	if !ok {
		return false
	}
	_, err := raw.ChatRaw(ctx, model, warmRequestBody(body), nil)
	return err == nil
}
