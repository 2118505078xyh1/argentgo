// Package telemetry 提供 stdlib-only 的观测契约（Span / Counter / Histogram /
// Provider）与共享 NOOP 单例。默认 Provider 丢弃所有调用、零分配，不改变任何
// 现有事件或日志字节；opt-in 的实际适配由 app 注入。
package telemetry

import (
	"context"
	"sync/atomic"
	"time"
)

// Span 表示一次有界操作。
type Span interface {
	SetAttribute(key, value string)
	End()
}

// Counter 是单调累加计数器。
type Counter interface {
	Add(delta float64)
}

// Histogram 记录分布样本。
type Histogram interface {
	Record(value float64)
}

// Provider 构造 Span / Metric。
type Provider interface {
	Tracer() Tracer
	Counter(name string) Counter
	Histogram(name string) Histogram
}

// Tracer 开启命名跨度。
type Tracer interface {
	Start(ctx context.Context, name string) (context.Context, Span)
}

// --- NOOP 实现 ---

type noopSpan struct{}

func (noopSpan) SetAttribute(string, string) {}
func (noopSpan) End()                        {}

type noopCounter struct{}

func (noopCounter) Add(float64) {}

type noopHistogram struct{}

func (noopHistogram) Record(float64) {}

type noopTracer struct{}

func (noopTracer) Start(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopProvider struct{}

func (noopProvider) Tracer() Tracer             { return noopTracer{} }
func (noopProvider) Counter(string) Counter     { return noopCounter{} }
func (noopProvider) Histogram(string) Histogram { return noopHistogram{} }

var noop noopProvider

var current atomic.Value // 存放 Provider

// SetProvider 替换全局 Provider；nil 回退 NOOP。
func SetProvider(p Provider) {
	if p == nil {
		current.Store(noop)
		return
	}
	current.Store(p)
}

// Current 返回当前 Provider；未设置时返回 NOOP 单例。
func Current() Provider {
	if p, ok := current.Load().(Provider); ok {
		return p
	}
	return noop
}

// StartSpan 用当前 Provider 在 ctx 上开启命名跨度。默认 NOOP 时零分配。
func StartSpan(ctx context.Context, name string) (context.Context, Span) {
	return Current().Tracer().Start(ctx, name)
}

// ElapsedMS 把时间区间换算成毫秒浮点。纯函数。
func ElapsedMS(start, end time.Time) float64 {
	return float64(end.Sub(start)) / float64(time.Millisecond)
}
