package bash

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestWithSafetyOracleInjectAndRead 检查 ctx 注入点的注入与读取语义：
// 注入后 safetyOracleFrom 返回可用谓词；未注入或传 nil 时得到 nil。
func TestWithSafetyOracleInjectAndRead(t *testing.T) {
	// 未注入的 ctx 应读到 nil（默认放行）。
	if got := safetyOracleFrom(context.Background()); got != nil {
		t.Fatalf("未注入的 ctx: safetyOracleFrom = 非 nil，期望 nil")
	}
	if got := safetyOracleFrom(nil); got != nil {
		t.Fatalf("nil ctx: safetyOracleFrom = 非 nil，期望 nil")
	}

	fn := func(cmd string) bool { return !strings.Contains(cmd, "danger") }
	ctx := WithSafetyOracle(context.Background(), fn)
	got := safetyOracleFrom(ctx)
	if got == nil {
		t.Fatal("注入后 safetyOracleFrom = nil，期望非 nil")
	}
	if got("echo safe") != true || got("echo danger") != false {
		t.Fatalf("注入的谓词行为不符：safe=%v danger=%v", got("echo safe"), got("echo danger"))
	}

	// nil 谓词是空操作，不污染 ctx。
	if WithSafetyOracle(ctx, nil) != ctx {
		t.Fatal("WithSafetyOracle(ctx, nil) 应原样返回 ctx")
	}
}

// TestCurrentSafetyDefaultAllows 检查默认态：ctx 未注入且未传显式谓词时
// CurrentSafety 返回 true，保证既有运行语义逐字节不变。
func TestCurrentSafetyDefaultAllows(t *testing.T) {
	for _, cmd := range []string{"", "echo hello", "回显 你好", "echo 'a; b'"} {
		if !CurrentSafety(context.Background(), nil, cmd) {
			t.Fatalf("默认态 CurrentSafety(ctx, nil, %q) = false，期望 true（放行）", cmd)
		}
	}
}

// TestCurrentSafetyStates 覆盖 CurrentSafety 的各态：
// 显式谓词优先、ctx 中 oracle 拒绝/放行、默认放行。
func TestCurrentSafetyStates(t *testing.T) {
	// oracle：仅当命令含 "danger" 时拒绝。
	ctx := WithSafetyOracle(context.Background(), func(cmd string) bool {
		return !strings.Contains(cmd, "danger")
	})

	tests := []struct {
		name     string
		ctx      context.Context
		explicit func(string) bool
		command  string
		want     bool
	}{
		{"oracle rejects", ctx, nil, "echo danger", false},
		{"oracle allows", ctx, nil, "echo safe", true},
		{"explicit wins reject", ctx, func(string) bool { return false }, "echo safe", false},
		{"explicit wins allow", ctx, func(string) bool { return true }, "echo danger", true},
		{"no oracle default allows", context.Background(), nil, "echo danger", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {

			if got := CurrentSafety(tc.ctx, tc.explicit, tc.command); got != tc.want {
				t.Fatalf("CurrentSafety(%s, %q) = %v, want %v", tc.name, tc.command, got, tc.want)
			}
		})
	}
}

// oracleCall 记录一次安全咨询：run 是谓词所属的运行，cmd 是被咨询的命令。
type oracleCall struct {
	run string
	cmd string
}

// callLog 是并发安全咨询日志。假 chain/oracle 用它记录 Check 调用，
// 断言每个运行只用到了自己的谓词。
type callLog struct {
	mu    sync.Mutex
	calls []oracleCall
}

func (l *callLog) add(run, cmd string) {
	l.mu.Lock()
	l.calls = append(l.calls, oracleCall{run: run, cmd: cmd})
	l.mu.Unlock()
}

func (l *callLog) snapshot() []oracleCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]oracleCall(nil), l.calls...)
}

// TestSafetyOracleIsolatesConcurrentRuns 是本修复的核心回归测试：
// 两个"运行"各自把自己的谓词注入独立 ctx，并并发发起安全咨询。
// 旧实现用进程级 atomic.Value，后注入的谓词会覆盖前者，导致 A 的 bash
// 拿到 B 的 chain/ctx（审计串味、用已取消 ctx 误拒）。现在谓词随 ctx
// 传播，每个运行只能读到自己的谓词。断言两件事：
//  1. 每次咨询结果都正确（用只认本运行前缀的谓词）；
//  2. 日志里没有跨运行的调用（A 的谓词没被 B 用到，反之亦然）。
func TestSafetyOracleIsolatesConcurrentRuns(t *testing.T) {
	logA, logB := &callLog{}, &callLog{}

	ctxA := WithSafetyOracle(context.Background(), func(cmd string) bool {
		logA.add("A", cmd)
		return strings.HasPrefix(cmd, "A:")
	})
	ctxB := WithSafetyOracle(context.Background(), func(cmd string) bool {
		logB.add("B", cmd)
		return strings.HasPrefix(cmd, "B:")
	})

	// 每条 case 只传本运行前缀的命令。各运行的谓词只认自己的前缀：
	// 若串味（A 用了 B 的谓词），A 的命令会被 B 拒绝，结果必然出错。
	tests := []struct {
		name    string
		ctx     context.Context
		command string
		want    bool
	}{
		{"A run", ctxA, "A: echo safe", true},
		{"B run", ctxB, "B: echo safe", true},
	}

	const rounds = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, tc := range tests {
		for r := 0; r < rounds; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // 尽量让所有 goroutine 同时开跑，放大串味窗口
				if got := CurrentSafety(tc.ctx, nil, tc.command); got != tc.want {
					t.Errorf("%s: CurrentSafety = %v, want %v", tc.name, got, tc.want)
				}
			}()
		}
	}
	close(start)
	wg.Wait()

	// 断言没有跨运行调用：A 的谓词只见过 "A:" 命令，B 的只见过 "B:"。
	for _, c := range logA.snapshot() {
		if !strings.HasPrefix(c.cmd, "A:") || c.run != "A" {
			t.Fatalf("运行 A 的谓词被跨运行调用：run=%s cmd=%q", c.run, c.cmd)
		}
	}
	for _, c := range logB.snapshot() {
		if !strings.HasPrefix(c.cmd, "B:") || c.run != "B" {
			t.Fatalf("运行 B 的谓词被跨运行调用：run=%s cmd=%q", c.run, c.cmd)
		}
	}
	// 两个谓词都确实被调用过，避免空日志让断言形同虚设。
	if len(logA.snapshot()) == 0 || len(logB.snapshot()) == 0 {
		t.Fatal("并发咨询没有触发谓词，测试无效")
	}
}

// TestSafetyOracleNoProcessResidue 证明运行结束后 oracle 不残留：
// 注入了拒绝谓词的 ctx 用完后，全新的 ctx 仍然默认放行。
func TestSafetyOracleNoProcessResidue(t *testing.T) {
	ctx := WithSafetyOracle(context.Background(), func(string) bool { return false })
	if CurrentSafety(ctx, nil, "anything") {
		t.Fatal("注入拒绝谓词后 CurrentSafety 应返回 false")
	}
	// 模拟"运行结束"：ctx 离开作用域后，新 ctx 不应有任何残留。
	fresh := context.Background()
	if !CurrentSafety(fresh, nil, "anything") {
		t.Fatal("全新 ctx 应默认放行，oracle 不应残留")
	}
}

// TestCurrentSafetyCJK 检查含 CJK 的命令被原样交给谓词，字节不被拆分或改写。
func TestCurrentSafetyCJK(t *testing.T) {
	const cmd = "回显 你好 世界"
	ctx := WithSafetyOracle(context.Background(), func(got string) bool { return got == cmd })

	if !CurrentSafety(ctx, nil, cmd) {
		t.Fatalf("CJK 命令未被原样传递，CurrentSafety 拒绝: %q", cmd)
	}
	if CurrentSafety(ctx, nil, "回显 你好") {
		t.Fatal("不同 CJK 命令不应被误判为放行")
	}
}

// TestCurrentSafetyDoesNotModifyInput 记录并验证"不修改入参"契约：
// command 是字符串（不可变），显式 fn 与 ctx oracle 也不会被本文件改写。
func TestCurrentSafetyDoesNotModifyInput(t *testing.T) {
	original := "回显 $TOKEN 'a; b' > 文件"
	command := original

	ctx := WithSafetyOracle(context.Background(), func(got string) bool { return got == original })
	explicit := func(got string) bool { return got == original }

	if !CurrentSafety(ctx, explicit, command) {
		t.Fatal("原样命令应被放行")
	}
	if command != original {
		t.Fatalf("入参被修改: got %q, want %q", command, original)
	}
	// 去掉显式 fn，ctx oracle 同样应放行，且再次确认 command 未变。
	if !CurrentSafety(ctx, nil, command) {
		t.Fatal("ctx oracle 下原样命令应被放行")
	}
	if command != original {
		t.Fatalf("入参被修改: got %q, want %q", command, original)
	}
}
