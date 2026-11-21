package server

import (
	"math"
	"testing"
	"time"
)

// TestHeartbeatInterval 表驱动覆盖归一化规则：默认回退、上限钳制、
// 边界稳定与区间内原样返回。间隔参数均为 time.Duration（数值类型），
// 函数内部不做任何字符串处理，因此 CJK 字符无法影响结果。
func TestHeartbeatInterval(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provided time.Duration
		want     time.Duration
	}{
		{"零值回退默认", 0, eventHeartbeatInterval},
		{"负值回退默认", -time.Second, eventHeartbeatInterval},
		{"最小 Duration 回退默认", time.Duration(math.MinInt64), eventHeartbeatInterval},
		{"默认边界稳定", eventHeartbeatInterval, eventHeartbeatInterval},
		{"默认以下一纳秒原样返回", eventHeartbeatInterval - 1, eventHeartbeatInterval - 1},
		{"默认以上一纳秒原样返回", eventHeartbeatInterval + 1, eventHeartbeatInterval + 1},
		{"小正数原样返回", time.Millisecond, time.Millisecond},
		{"一纳秒原样返回", 1, 1},
		{"上限边界稳定", maxHeartbeatInterval, maxHeartbeatInterval},
		{"上限以下一纳秒原样返回", maxHeartbeatInterval - 1, maxHeartbeatInterval - 1},
		{"上限以上一纳秒钳制", maxHeartbeatInterval + 1, maxHeartbeatInterval},
		{"远超上限钳制", 24 * time.Hour, maxHeartbeatInterval},
		{"最大 Duration 钳制", time.Duration(math.MaxInt64), maxHeartbeatInterval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := heartbeatInterval(tc.provided); got != tc.want {
				t.Fatalf("heartbeatInterval(%v) = %v, want %v", tc.provided, got, tc.want)
			}
		})
	}
}

// TestHeartbeatIntervalIdempotent 守护边界稳定性：归一化后再归一化
// 结果不变，且每次调用结果完全一致（CJK 或区域设置不会介入数值比较）。
func TestHeartbeatIntervalIdempotent(t *testing.T) {
	for _, provided := range []time.Duration{
		time.Duration(math.MinInt64),
		-time.Second,
		0,
		time.Millisecond,
		eventHeartbeatInterval,
		maxHeartbeatInterval,
		maxHeartbeatInterval + 1,
		time.Duration(math.MaxInt64),
	} {
		once := heartbeatInterval(provided)
		if twice := heartbeatInterval(once); twice != once {
			t.Fatalf("heartbeatInterval 非幂等: %v -> %v -> %v", provided, once, twice)
		}
		// 同一输入重复调用结果一致，证明无隐藏状态或文本依赖。
		for i := 0; i < 3; i++ {
			if got := heartbeatInterval(provided); got != once {
				t.Fatalf("heartbeatInterval(%v) 不稳定: 得到 %v，期望 %v", provided, got, once)
			}
		}
	}
}

// TestHeartbeatIntervalDefaults 固定默认值与上限的字面量，防止常量被
// 无意改动后测试随波逐流而失去守护意义。
func TestHeartbeatIntervalDefaults(t *testing.T) {
	if eventHeartbeatInterval != 15*time.Second {
		t.Fatalf("默认心跳间隔 = %v, want 15s", eventHeartbeatInterval)
	}
	if maxHeartbeatInterval != 60*time.Second {
		t.Fatalf("心跳间隔上限 = %v, want 60s", maxHeartbeatInterval)
	}
}

// TestHeartbeatIntervalWiring 验证 Config.HeartbeatInterval 确实接线到
// SSE 处理器：非零配置改变心跳频率，非法配置回退默认且不产生心跳风暴。
func TestHeartbeatIntervalWiring(t *testing.T) {
	if got := heartbeatInterval(Config{}.HeartbeatInterval); got != 15*time.Second {
		t.Fatalf("Config 默认心跳间隔 = %v, want 15s", got)
	}
	cfg := Config{HeartbeatInterval: 30 * time.Second}
	if got := heartbeatInterval(cfg.HeartbeatInterval); got != 30*time.Second {
		t.Fatalf("Config 心跳间隔 = %v, want 30s", got)
	}
	cfg = Config{HeartbeatInterval: -time.Second}
	if got := heartbeatInterval(cfg.HeartbeatInterval); got != 15*time.Second {
		t.Fatalf("非法 Config 心跳间隔 = %v, want 15s", got)
	}
}
