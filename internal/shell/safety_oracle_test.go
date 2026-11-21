package shell

import (
	"strings"
	"sync"
	"testing"
)

// TestSafetyOracleSetAndClear 检查包级注入点的注入与清除语义：
// 注入后 safetyCheck 返回可用的谓词；传 nil 清除后重新得到 nil。
func TestSafetyOracleSetAndClear(t *testing.T) {
	t.Cleanup(func() { SetSafetyOracle(nil) })

	// 起点应为未注入（默认放行）。
	if got := safetyCheck(); got != nil {
		t.Fatalf("初始 safetyCheck() = 非 nil，期望 nil（默认未注入）")
	}

	SetSafetyOracle(func(cmd string) bool { return !strings.Contains(cmd, "danger") })
	fn := safetyCheck()
	if fn == nil {
		t.Fatal("注入后 safetyCheck() = nil，期望非 nil")
	}
	if fn("echo safe") != true || fn("echo danger") != false {
		t.Fatalf("注入的谓词行为不符：safe=%v danger=%v", fn("echo safe"), fn("echo danger"))
	}

	SetSafetyOracle(nil)
	if got := safetyCheck(); got != nil {
		t.Fatalf("清除后 safetyCheck() = 非 nil，期望 nil")
	}
}

// TestCurrentSafetyDefaultAllows 检查默认态：oracle 未注入且未传显式谓词时
// CurrentSafety 返回 true，保证既有运行语义逐字节不变。
func TestCurrentSafetyDefaultAllows(t *testing.T) {
	t.Cleanup(func() { SetSafetyOracle(nil) })
	SetSafetyOracle(nil)

	for _, cmd := range []string{"", "echo hello", "回显 你好", "echo 'a; b'"} {
		if !CurrentSafety(nil, cmd) {
			t.Fatalf("默认态 CurrentSafety(nil, %q) = false，期望 true（放行）", cmd)
		}
	}
}

// TestCurrentSafetyStates 覆盖 CurrentSafety 的各态：
// 显式谓词优先、oracle 拒绝、oracle 放行、默认放行。
func TestCurrentSafetyStates(t *testing.T) {
	t.Cleanup(func() { SetSafetyOracle(nil) })

	// oracle：仅当命令含 "danger" 时拒绝。
	SetSafetyOracle(func(cmd string) bool { return !strings.Contains(cmd, "danger") })

	tests := []struct {
		name     string
		explicit func(string) bool // 显式传入的 fn
		command  string
		want     bool
	}{
		{"oracle rejects", nil, "echo danger", false},
		{"oracle allows", nil, "echo safe", true},
		{"explicit wins reject", func(string) bool { return false }, "echo safe", false},
		{"explicit wins allow", func(string) bool { return true }, "echo danger", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CurrentSafety(tc.explicit, tc.command); got != tc.want {
				t.Fatalf("CurrentSafety(%s, %q) = %v, want %v", tc.name, tc.command, got, tc.want)
			}
		})
	}
}

// TestCurrentSafetyConcurrentSetAndCheck 并发地 SetSafetyOracle 与读取/CurrentSafety，
// 断言不 panic、无数据竞争（配合 -race）。所有注入的函数类型一致，
// 验证 atomic.Value 的类型一致性与并发安全。
func TestCurrentSafetyConcurrentSetAndCheck(t *testing.T) {
	t.Cleanup(func() { SetSafetyOracle(nil) })

	const goroutines = 32
	const iterations = 200

	allow := func(string) bool { return true }
	reject := func(string) bool { return false }

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// 一半 goroutine 反复切换注入/清除。
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				switch (i + j) % 4 {
				case 0:
					SetSafetyOracle(allow)
				case 1:
					SetSafetyOracle(reject)
				default:
					SetSafetyOracle(nil)
				}
			}
		}(i)
	}

	// 另一半 goroutine 反复读取与调用，校验返回值始终是合法 bool。
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = safetyCheck()
				// 显式谓词不依赖全局态，结果应稳定为 true。
				if !CurrentSafety(func(string) bool { return true }, "echo hi") {
					t.Errorf("显式放行谓词被并发注入意外改变")
					return
				}
				// 无显式谓词时，结论取决于现场 oracle，但必须是合法 bool，
				// 且不应 panic；这里只调用以触发潜在的竞争/panic。
				_ = CurrentSafety(nil, "echo hi")
			}
		}()
	}

	wg.Wait()
}

// TestCurrentSafetyCJK 检查含 CJK 的命令被原样交给谓词，字节不被拆分或改写。
func TestCurrentSafetyCJK(t *testing.T) {
	t.Cleanup(func() { SetSafetyOracle(nil) })

	const cmd = "回显 你好 世界"
	// 谓词逐字节核对整条命令，稍有截断/改写就会拒绝。
	SetSafetyOracle(func(got string) bool { return got == cmd })

	if !CurrentSafety(nil, cmd) {
		t.Fatalf("CJK 命令未被原样传递，CurrentSafety 拒绝: %q", cmd)
	}
	if CurrentSafety(nil, "回显 你好") {
		t.Fatalf("不同 CJK 命令不应被误判为放行")
	}
}

// TestCurrentSafetyDoesNotModifyInput 记录并验证"不修改入参"契约：
// command 是字符串（不可变），显式 fn 与 oracle 也不会被本文件改写。
func TestCurrentSafetyDoesNotModifyInput(t *testing.T) {
	t.Cleanup(func() { SetSafetyOracle(nil) })

	original := "回显 $TOKEN 'a; b' > 文件"
	command := original

	// 用会读取 command 的谓词，确认读到的是原始内容。
	SetSafetyOracle(func(got string) bool { return got == original })
	explicit := func(got string) bool { return got == original }

	if !CurrentSafety(explicit, command) {
		t.Fatal("原样命令应被放行")
	}
	if command != original {
		t.Fatalf("入参被修改: got %q, want %q", command, original)
	}
	// 显式 fn 优先，结果同样应放行，且再次确认 command 未变。
	if !CurrentSafety(explicit, command) {
		t.Fatal("显式谓词下原样命令应被放行")
	}
	if command != original {
		t.Fatalf("入参被修改: got %q, want %q", command, original)
	}
}
