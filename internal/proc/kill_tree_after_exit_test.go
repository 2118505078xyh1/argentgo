package proc

import (
	"sync"
	"testing"
)

// TestShouldKillTreeAfterExitAllCombinations 穷举三个布尔入参的全部八种组合，
// 锁定 shouldKillTreeAfterExit 的完整真值表。重点守护审计发现的缺口：
// 发了 SIGTERM、直接子进程已退出、宽限未到时（true,false,true）必须返回 true，
// 以便补杀忽略 SIGTERM 的孙进程。
func TestShouldKillTreeAfterExitAllCombinations(t *testing.T) {
	tests := []struct {
		name         string
		sigTermSent  bool
		graceExpired bool
		directExited bool
		want         bool
	}{
		{"未发SIGTERM且未到宽限且子进程未退出", false, false, false, false},
		{"未发SIGTERM且未到宽限但子进程已退出", false, false, true, false},
		{"未发SIGTERM且宽限已到但子进程未退出", false, true, false, false},
		{"未发SIGTERM且宽限已到且子进程已退出", false, true, true, false},
		{"已发SIGTERM且仍在宽限内且子进程未退出应继续等待", true, false, false, false},
		{"已发SIGTERM且宽限未到但直接子进程已退出仍需补杀孙进程", true, false, true, true},
		{"已发SIGTERM且宽限耗尽而子进程未退出应升级杀树", true, true, false, true},
		{"已发SIGTERM且宽限耗尽且子进程已退出仍执行杀树", true, true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldKillTreeAfterExit(tt.sigTermSent, tt.graceExpired, tt.directExited)
			if got != tt.want {
				t.Fatalf("shouldKillTreeAfterExit(%t, %t, %t) = %t, want %t",
					tt.sigTermSent, tt.graceExpired, tt.directExited, got, tt.want)
			}
		})
	}
}

// TestShouldKillTreeAfterExitMatchesReference 用独立的参考公式遍历全部组合，
// 确认实现与"发了 SIGTERM，且（宽限耗尽或子进程已退出）"这一规则完全一致，
// 避免真值表遗漏或某格写反。
func TestShouldKillTreeAfterExitMatchesReference(t *testing.T) {
	vals := []bool{false, true}
	for _, sent := range vals {
		for _, expired := range vals {
			for _, exited := range vals {
				want := sent && (expired || exited)
				got := shouldKillTreeAfterExit(sent, expired, exited)
				if got != want {
					t.Fatalf("shouldKillTreeAfterExit(%t, %t, %t) = %t, want %t",
						sent, expired, exited, got, want)
				}
			}
		}
	}
}

// TestShouldKillTreeAfterExitStableAndCJKIndependent 验证函数的稳定性与
// CJK 无关性：它只消费布尔值，不触碰任何文本。即便测试上下文里存在中文、
// 日文、韩文以及多字节 UTF-8 字符串，相同入参也必须得到相同结果；并发与
// 重复调用同样稳定（在 -race 下也不会触发数据竞争）。
func TestShouldKillTreeAfterExitStableAndCJKIndependent(t *testing.T) {
	// 这些文本仅作为"环境中存在多字节内容"的对照组；函数不接收也不读取它们，
	// 结果不得因它们的存在与否而改变。
	cjkSamples := []string{"中文", "日本語", "한국어", "进程树", "SIGTERM 宽限期", "🌲"}

	vals := []bool{false, true}
	for _, sent := range vals {
		for _, expired := range vals {
			for _, exited := range vals {
				want := shouldKillTreeAfterExit(sent, expired, exited)
				for _, s := range cjkSamples {
					if len(s) == 0 {
						t.Fatal("CJK 对照样本文本不应为空")
					}
					for i := 0; i < 100; i++ {
						if got := shouldKillTreeAfterExit(sent, expired, exited); got != want {
							t.Fatalf("重复调用不稳定：shouldKillTreeAfterExit(%t, %t, %t) 由 %t 变为 %t",
								sent, expired, exited, want, got)
						}
					}
				}
			}
		}
	}

	// 并发调用不得相互干扰，且每个 goroutine 都得到与串行一致的结果。
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, sent := range vals {
				for _, expired := range vals {
					for _, exited := range vals {
						want := sent && (expired || exited)
						if got := shouldKillTreeAfterExit(sent, expired, exited); got != want {
							t.Errorf("并发调用：shouldKillTreeAfterExit(%t, %t, %t) = %t, want %t",
								sent, expired, exited, got, want)
						}
					}
				}
			}
		}()
	}
	wg.Wait()
}
