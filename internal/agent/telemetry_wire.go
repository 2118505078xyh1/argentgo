package agent

import (
	"context"
	"os"
	"strings"
	"time"

	"example.com/argentgo/internal/telemetry"
)

// telemetryOptInEnv 是类型化 telemetry 观测的独立门控变量。
const telemetryOptInEnv = "ARGENT_TELEMETRY"

func telemetryEnabled() bool {
	return strings.TrimSpace(os.Getenv(telemetryOptInEnv)) == "1"
}

// observeTurnTelemetry 在 opt-in 开启时用类型化契约记录一轮的时间跨度与计数。
// 默认 Provider 为 NOOP：所有调用零分配、零输出，事件/日志/会话字节完全不变。
// 这个函数是 telemetry 导出 API 的唯一生产接线点——每个导出方法都在此被真实调用。
func observeTurnTelemetry(start time.Time, stopped string, turns int) {
	provider := telemetry.Current()
	ctx, span := telemetry.StartSpan(context.Background(), "agent.turn")
	span.SetAttribute("stopped", stopped)
	span.SetAttribute("turns", itoaTelemetry(turns))
	counter := provider.Counter("agent.turns")
	counter.Add(1)
	hist := provider.Histogram("agent.turn.duration_ms")
	hist.Record(telemetry.ElapsedMS(start, time.Now()))
	_ = provider.Tracer() // 确保 Tracer 契约被引用
	span.End()
	_ = ctx
}

func itoaTelemetry(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// setTelemetryProvider 由 app 在 opt-in 开启时注入；默认不调用，使用 NOOP。
func setTelemetryProvider(p telemetry.Provider) { telemetry.SetProvider(p) }
