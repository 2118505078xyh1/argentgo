package app

import (
	"testing"
	"time"
	"unicode/utf8"
)

// TestCacheMissReasonModelSwitched 覆盖模型切换分支：只要前后模型名按字节不同，
// 无论空闲时长与 TTL 如何，都归因于 model_switched；包括一侧为空串的情况。
func TestCacheMissReasonModelSwitched(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prevModel string
		curModel  string
		idle      time.Duration
		ttl       time.Duration
	}{
		{"plain names", "claude-sonnet", "claude-opus", 0, 0},
		{"idle below ttl", "a", "b", time.Minute, time.Hour},
		{"idle above ttl", "a", "b", 2 * time.Hour, time.Hour},
		{"prev empty", "", "gpt-4o", 0, time.Hour},
		{"cur empty", "gpt-4o", "", 0, time.Hour},
		{"case sensitive", "Model", "model", 0, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheMissReason(tc.prevModel, tc.curModel, tc.idle, tc.ttl); got != cacheMissModelSwitched {
				t.Fatalf("cacheMissReason(%q, %q, %v, %v) = %q, want %q",
					tc.prevModel, tc.curModel, tc.idle, tc.ttl, got, cacheMissModelSwitched)
			}
		})
	}
}

// TestCacheMissReasonIdleExpired 覆盖空闲过期分支：模型名相同时，只有空闲严格超过
// TTL 才归因于 idle_expired；恰好等于 TTL 或更短时不算过期。
func TestCacheMissReasonIdleExpired(t *testing.T) {
	for _, tc := range []struct {
		name string
		idle time.Duration
		ttl  time.Duration
	}{
		{"well over ttl", 2 * time.Hour, time.Hour},
		{"one nanosecond over", time.Hour + time.Nanosecond, time.Hour},
		{"zero ttl positive idle", time.Minute, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheMissReason("same-model", "same-model", tc.idle, tc.ttl); got != cacheMissIdleExpired {
				t.Fatalf("cacheMissReason(same, same, %v, %v) = %q, want %q",
					tc.idle, tc.ttl, got, cacheMissIdleExpired)
			}
		})
	}
}

// TestCacheMissReasonNoMiss 覆盖无需归因的分支：模型相同且空闲未超过 TTL 时返回空串，
// 包含空闲恰好等于 TTL 的边界、空闲为负（时钟回拨）以及两侧模型名都为空的情况。
func TestCacheMissReasonNoMiss(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prevModel string
		curModel  string
		idle      time.Duration
		ttl       time.Duration
	}{
		{"idle below ttl", "m", "m", time.Minute, time.Hour},
		{"idle equals ttl", "m", "m", time.Hour, time.Hour},
		{"both idle and ttl zero", "m", "m", 0, 0},
		{"negative idle", "m", "m", -time.Minute, time.Hour},
		{"both models empty", "", "", time.Minute, time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheMissReason(tc.prevModel, tc.curModel, tc.idle, tc.ttl); got != "" {
				t.Fatalf("cacheMissReason(%q, %q, %v, %v) = %q, want empty",
					tc.prevModel, tc.curModel, tc.idle, tc.ttl, got)
			}
		})
	}
}

// TestCacheMissReasonPrecedence 保证判定顺序稳定：模型切换与空闲超时同时成立时，
// 模型切换优先，绝不被空闲过期覆盖。
func TestCacheMissReasonPrecedence(t *testing.T) {
	got := cacheMissReason("old-model", "new-model", 24*time.Hour, time.Hour)
	if got != cacheMissModelSwitched {
		t.Fatalf("模型切换应优先于空闲超时，得到 %q，want %q", got, cacheMissModelSwitched)
	}
}

// TestCacheMissReasonCJKModelNames 保证 CJK 模型名安全：按完整 UTF-8 序列精确比较，
// 不同的中文模型名被判为切换，相同的中文模型名不会误判；返回值始终是合法 ASCII 标签，
// 不会把模型名或其片段带入结果。
func TestCacheMissReasonCJKModelNames(t *testing.T) {
	for _, tc := range []struct {
		name      string
		prevModel string
		curModel  string
		want      string
	}{
		{"different CJK names", "通义千问", "文心一言", cacheMissModelSwitched},
		{"same CJK name", "通义千问", "通义千问", ""},
		{"one extra CJK rune", "模型", "模型甲", cacheMissModelSwitched},
		{"mixed script", "claude-中文", "claude-日文", cacheMissModelSwitched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := cacheMissReason(tc.prevModel, tc.curModel, 0, time.Hour)
			if got != tc.want {
				t.Fatalf("cacheMissReason(%q, %q) = %q, want %q", tc.prevModel, tc.curModel, got, tc.want)
			}
			if !utf8.ValidString(got) || utf8.RuneCountInString(got) != len(got) {
				t.Fatalf("返回值不是合法 ASCII：%q", got)
			}
		})
	}
}

// TestCacheMissReasonStable 保证纯函数特性：同一组输入多次调用逐字节一致，
// 且返回值只可能是指定的常量之一，不会因调用次序或状态而漂移。
func TestCacheMissReasonStable(t *testing.T) {
	inputs := []struct {
		prevModel string
		curModel  string
		idle      time.Duration
		ttl       time.Duration
	}{
		{"a", "b", 0, 0},
		{"a", "a", 2 * time.Hour, time.Hour},
		{"通义千问", "通义千问", time.Minute, time.Hour},
	}
	for _, in := range inputs {
		first := cacheMissReason(in.prevModel, in.curModel, in.idle, in.ttl)
		switch first {
		case cacheMissModelSwitched, cacheMissIdleExpired, "":
		default:
			t.Fatalf("返回了未知归因标签 %q", first)
		}
		for i := 0; i < 3; i++ {
			if got := cacheMissReason(in.prevModel, in.curModel, in.idle, in.ttl); got != first {
				t.Fatalf("cacheMissReason 不稳定：首次 %q，第 %d 次 %q", first, i+1, got)
			}
		}
	}
}
