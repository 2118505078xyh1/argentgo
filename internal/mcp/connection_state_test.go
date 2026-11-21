package mcp

import (
	"errors"
	"sync"
	"testing"
)

// mustReach 把一台零值 FSM 沿合法路径驱动到 target，并返回当时的生成号。
// 路径走的全是合法转换，因此任何失败都是测试前置条件被破坏。
func mustReach(t *testing.T, f *ConnFsm, target ConnState) uint64 {
	t.Helper()
	paths := map[ConnState][]ConnState{
		ConnDisconnected: {},
		ConnConnecting:   {ConnConnecting},
		ConnFailed:       {ConnConnecting, ConnFailed},
		ConnReady:        {ConnConnecting, ConnReady},
		ConnClosed:       {ConnClosed},
	}
	steps, ok := paths[target]
	if !ok {
		t.Fatalf("mustReach: 未知目标状态 %q", target)
	}
	for _, step := range steps {
		_, gen := f.State()
		if err := f.Transition(step, gen); err != nil {
			t.Fatalf("mustReach: 驱动到 %q 时转换到 %q 失败: %v", target, step, err)
		}
	}
	_, gen := f.State()
	return gen
}

// TestConnFsmZeroValue 验证零值可用：状态归一化为 disconnected，生成号为 0。
func TestConnFsmZeroValue(t *testing.T) {
	var f ConnFsm
	got, gen := f.State()
	if got != ConnDisconnected || gen != 0 {
		t.Errorf("零值 State() = (%q, %d), want (%q, 0)", got, gen, ConnDisconnected)
	}
}

// TestConnFsmTransitionMatrix 表驱动穷举 5x5 状态对，验证合法矩阵与生成号推进：
// 合法转换无错误且 gen+1，非法转换返回 ErrInvalidTransition 且状态、gen 均不变。
func TestConnFsmTransitionMatrix(t *testing.T) {
	states := []ConnState{
		ConnDisconnected, ConnConnecting, ConnReady, ConnFailed, ConnClosed,
	}
	allowed := map[ConnState]map[ConnState]bool{
		ConnDisconnected: {ConnConnecting: true, ConnClosed: true},
		ConnConnecting:   {ConnReady: true, ConnFailed: true, ConnClosed: true},
		ConnReady:        {ConnFailed: true, ConnClosed: true},
		ConnFailed:       {ConnConnecting: true, ConnClosed: true},
		ConnClosed:       {},
	}

	for _, from := range states {
		for _, to := range states {
			name := string(from) + "->" + string(to)
			t.Run(name, func(t *testing.T) {
				var f ConnFsm
				gen := mustReach(t, &f, from)
				wantOK := allowed[from][to]

				err := f.Transition(to, gen)
				gotState, gotGen := f.State()
				if wantOK {
					if err != nil {
						t.Fatalf("合法转换返回错误: %v", err)
					}
					if gotState != to {
						t.Errorf("状态 = %q, want %q", gotState, to)
					}
					if gotGen != gen+1 {
						t.Errorf("生成号 = %d, want %d", gotGen, gen+1)
					}
					return
				}
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("非法转换 err = %v, want ErrInvalidTransition", err)
				}
				if gotState != from || gotGen != gen {
					t.Errorf("非法转换改变了状态: got (%q, %d), want (%q, %d)",
						gotState, gotGen, from, gen)
				}
			})
		}
	}
}

// TestConnFsmGenerationMonotonic 沿一条合法路径推进，验证生成号每次恰好 +1、
// 全程单调递增，且状态与预期路径一致。
func TestConnFsmGenerationMonotonic(t *testing.T) {
	wantPath := []ConnState{
		ConnDisconnected, ConnConnecting, ConnReady, ConnFailed, ConnConnecting, ConnClosed,
	}

	var f ConnFsm
	for i, wantState := range wantPath {
		gotState, gotGen := f.State()
		if gotState != wantState {
			t.Fatalf("第 %d 步状态 = %q, want %q", i, gotState, wantState)
		}
		if gotGen != uint64(i) {
			t.Fatalf("第 %d 步生成号 = %d, want %d", i, gotGen, i)
		}
		if i == len(wantPath)-1 {
			break
		}
		next := wantPath[i+1]
		if err := f.Transition(next, gotGen); err != nil {
			t.Fatalf("第 %d 步转换到 %q 失败: %v", i, next, err)
		}
	}

	// 终态之后任何尝试都不应再推进生成号。
	finalState, finalGen := f.State()
	if err := f.Transition(ConnConnecting, finalGen); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("closed 之后转换 err = %v, want ErrInvalidTransition", err)
	}
	if s, g := f.State(); s != finalState || g != finalGen {
		t.Errorf("终态被改变: got (%q, %d), want (%q, %d)", s, g, finalState, finalGen)
	}
}

// TestConnFsmStaleGeneration 表驱动验证生成号守卫：观测过期时一律拒绝，状态和
// 生成号不变；并确认“过期优先于非法”——过期观测即使目标非法也报 ErrStaleGeneration。
func TestConnFsmStaleGeneration(t *testing.T) {
	tests := []struct {
		name   string
		to     ConnState
		gen    uint64
		wantIs error
	}{
		{name: "过小的生成号", to: ConnReady, gen: 0, wantIs: ErrStaleGeneration},
		{name: "过大的生成号", to: ConnReady, gen: 99, wantIs: ErrStaleGeneration},
		{name: "过期优先于非法目标", to: ConnState("bogus"), gen: 0, wantIs: ErrStaleGeneration},
		{name: "生成号匹配才检查合法性", to: ConnState("bogus"), gen: 1, wantIs: ErrInvalidTransition},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f ConnFsm
			mustReach(t, &f, ConnConnecting) // 当前 gen=1, state=connecting
			beforeState, beforeGen := f.State()

			err := f.Transition(tt.to, tt.gen)
			if !errors.Is(err, tt.wantIs) {
				t.Errorf("err = %v, want %v", err, tt.wantIs)
			}
			if s, g := f.State(); s != beforeState || g != beforeGen {
				t.Errorf("过期转换改变了状态: got (%q, %d), want (%q, %d)",
					s, g, beforeState, beforeGen)
			}
		})
	}
}

// TestConnFsmUnknownStateCJKIrrelevant 验证状态判定完全由字符串值与转换矩阵决定，
// 与文本语言无关：CJK、英文或混合的未知状态一律被同等地拒绝；而与合法常量取值
// 完全相同的字符串字面量则与常量行为一致、正常被接受。
func TestConnFsmUnknownStateCJKIrrelevant(t *testing.T) {
	unknowns := []ConnState{
		ConnState("bogus"),
		ConnState("连接中"),
		ConnState("ready中文"),
		ConnState("连接-Connecting"),
	}
	for _, u := range unknowns {
		t.Run("拒绝_"+string(u), func(t *testing.T) {
			var f ConnFsm
			gen := mustReach(t, &f, ConnConnecting)
			err := f.Transition(u, gen)
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("未知状态 %q: err = %v, want ErrInvalidTransition", u, err)
			}
		})
	}

	// 合法字符串字面量（值为 "ready"）与常量 ConnReady 完全等价：从 connecting
	// 出发用字面量转换应成功，证明接纳路径同样不依赖任何语言/文本特判。
	t.Run("接受合法字面量", func(t *testing.T) {
		var f ConnFsm
		gen := mustReach(t, &f, ConnConnecting)
		if err := f.Transition(ConnState("ready"), gen); err != nil {
			t.Errorf("用字符串字面量 \"ready\" 转换失败: %v", err)
		}
		if s, g := f.State(); s != ConnReady || g != gen+1 {
			t.Errorf("State() = (%q, %d), want (%q, %d)", s, g, ConnReady, gen+1)
		}
	})

	if ConnState("ready") != ConnReady || ConnState("连接中") == ConnReady {
		t.Error("ConnState 的相等性应完全基于字符串值，与语言无关")
	}
}

// TestConnFsmConcurrentStateWhileTransition 在多个读者与单个写入者并发下验证
// State 观察到的生成号单调不减，且写入次数与最终生成号一致。供 -race 运行。
func TestConnFsmConcurrentStateWhileTransition(t *testing.T) {
	const readers = 8
	const iterations = 500

	var f ConnFsm
	cycle := map[ConnState]ConnState{
		ConnDisconnected: ConnConnecting,
		ConnConnecting:   ConnReady,
		ConnReady:        ConnFailed,
		ConnFailed:       ConnConnecting,
	}

	var wg sync.WaitGroup
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last uint64
			for i := 0; i < iterations; i++ {
				_, gen := f.State()
				if gen < last {
					t.Errorf("读者观察到生成号倒退: %d < %d", gen, last)
				}
				last = gen
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			state, gen := f.State()
			to, ok := cycle[state]
			if !ok {
				t.Errorf("写入者遇到非循环状态 %q", state)
				return
			}
			if err := f.Transition(to, gen); err != nil {
				t.Errorf("写入者转换 %q->%q 失败: %v", state, to, err)
				return
			}
		}
	}()
	wg.Wait()

	if _, gen := f.State(); gen != iterations {
		t.Errorf("最终生成号 = %d, want %d", gen, iterations)
	}
}

// TestConnFsmConcurrentCompetingTransitions 多个写入者竞争推进，验证生成号守卫
// 下最终生成号严格等于成功转换次数，不因竞争而丢失或重复计数。供 -race 运行。
func TestConnFsmConcurrentCompetingTransitions(t *testing.T) {
	const writers = 8
	const iterations = 300

	var f ConnFsm
	cycle := map[ConnState]ConnState{
		ConnDisconnected: ConnConnecting,
		ConnConnecting:   ConnReady,
		ConnReady:        ConnFailed,
		ConnFailed:       ConnConnecting,
	}

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
	)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				state, gen := f.State()
				to, ok := cycle[state]
				if !ok {
					return
				}
				if err := f.Transition(to, gen); err == nil {
					mu.Lock()
					succeeded++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if _, gen := f.State(); gen != uint64(succeeded) {
		t.Errorf("最终生成号 = %d, 成功转换次数 = %d, 应相等", gen, succeeded)
	}
}
