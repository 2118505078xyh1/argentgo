package llm

import (
	"context"
	"testing"
)

// BenchmarkRequestSnapshotNoop 量化默认（未挂收集器）时快照钩子的开销。
// 这是一次 context 查找 + 类型断言，必须保持在纳秒级。
func BenchmarkRequestSnapshotNoop(b *testing.B) {
	ctx := context.Background()
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		recordRequestSnapshot(ctx, ProtocolChatCompletions, "m", body)
	}
}

// BenchmarkRequestSnapshotWithSink 量化 opt-in 开启时（no-op 收集器）的开销，
// 用于确认钩子本身不会成为请求热路径的瓶颈。
func BenchmarkRequestSnapshotWithSink(b *testing.B) {
	ctx := WithRequestSnapshotSink(context.Background(), func(string, string, []byte) error { return nil })
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		recordRequestSnapshot(ctx, ProtocolChatCompletions, "m", body)
	}
}
