package agent

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// repeatedSteps 构造 n 步签名完全相同的工具调用。
func repeatedSteps(n int) []ToolStep {
	steps := make([]ToolStep, n)
	for i := range steps {
		steps[i] = ToolStep{Name: "shell", Input: "go test ./...", Output: "ok"}
	}
	return steps
}

func TestDetectLoopSameSignatureOverThreshold(t *testing.T) {
	// 默认窗口 10、阈值 5：6 步相同签名应当检出，count 为 6。
	steps := repeatedSteps(6)
	sig, count, detected := DetectLoop(steps, 0, 0)
	if !detected {
		t.Fatal("6 identical signatures should be detected with defaults")
	}
	if count != 6 {
		t.Fatalf("count = %d, want 6", count)
	}
	if want := stepSignature(steps[0]); sig != want {
		t.Fatalf("signature = %q, want %q", sig, want)
	}
}

func TestDetectLoopSameNameDifferentOutputNoFalsePositive(t *testing.T) {
	// 工具名与输入相同，但输出各不相同 → 签名各异，不应误报。
	steps := make([]ToolStep, 8)
	for i := range steps {
		steps[i] = ToolStep{Name: "read", Input: "file.go", Output: string(rune('a' + i))}
	}
	if _, _, detected := DetectLoop(steps, 0, 0); detected {
		t.Fatal("same name but different outputs must not be flagged as a loop")
	}

	// 同样地，仅输入不同也不应误报。
	steps = make([]ToolStep, 8)
	for i := range steps {
		steps[i] = ToolStep{Name: "read", Input: string(rune('a' + i)), Output: "ok"}
	}
	if _, _, detected := DetectLoop(steps, 0, 0); detected {
		t.Fatal("same name but different inputs must not be flagged as a loop")
	}
}

func TestDetectLoopWindowBoundary(t *testing.T) {
	// 前 6 步相同（循环），后 20 步互不相同，共 26 步。
	steps := repeatedSteps(6)
	for i := 0; i < 20; i++ {
		steps = append(steps, ToolStep{Name: "edit", Input: string(rune('A' + i)), Output: "done"})
	}

	// 窗口 6 只看尾部 6 步（全不同）→ 不检出。
	if _, _, detected := DetectLoop(steps, 6, 0); detected {
		t.Fatal("window of 6 excludes the early loop and must not detect")
	}
	// 窗口 25 只看到末尾 25 步，循环的前 6 步已被排除 1 步 → 剩余相同签名 5 步，不超阈值。
	if _, _, detected := DetectLoop(steps, 25, 0); detected {
		t.Fatal("window of 25 leaves only 5 identical steps, must not detect")
	}
	// 窗口覆盖全部 26 步 → 检出，count 为 6。
	sig, count, detected := DetectLoop(steps, len(steps), 0)
	if !detected || count != 6 {
		t.Fatalf("large window: detected=%v count=%d, want true/6", detected, count)
	}
	if want := stepSignature(steps[0]); sig != want {
		t.Fatalf("signature = %q, want %q", sig, want)
	}

	// 循环位于尾部时，小窗口也能检出。
	tail := make([]ToolStep, 0, 26)
	for i := 0; i < 20; i++ {
		tail = append(tail, ToolStep{Name: "edit", Input: string(rune('A' + i)), Output: "done"})
	}
	tail = append(tail, repeatedSteps(6)...)
	if _, count, detected := DetectLoop(tail, 6, 0); !detected || count != 6 {
		t.Fatalf("tail loop with window 6: detected=%v count=%d, want true/6", detected, count)
	}
}

func TestDetectLoopThresholdBoundary(t *testing.T) {
	steps := repeatedSteps(6)
	// 阈值 5：6 > 5，检出。
	if _, count, detected := DetectLoop(steps, 0, 5); !detected || count != 6 {
		t.Fatalf("threshold 5: detected=%v count=%d, want true/6", detected, count)
	}
	// 阈值 6：6 > 6 为假，不检出。
	if _, _, detected := DetectLoop(steps, 0, 6); detected {
		t.Fatal("threshold 6: count 6 must not exceed threshold")
	}
	// 阈值 7 同理不检出。
	if _, _, detected := DetectLoop(steps, 0, 7); detected {
		t.Fatal("threshold 7: count 6 must not exceed threshold")
	}
}

func TestDetectLoopCJKAndSpecialCharacters(t *testing.T) {
	// CJK、emoji、NUL 与引号等特殊字节均应被完整纳入签名，且计算确定。
	step := ToolStep{Name: "编辑", Input: "路径\x00含\"引号\"与emoji🛠️", Output: "成功：已写入 3 行"}
	sig := stepSignature(step)
	if sig == "" || len(sig) != 64 {
		t.Fatalf("signature = %q, want 64-char hex sha256", sig)
	}
	if sig != stepSignature(step) {
		t.Fatal("signature of the same step must be deterministic")
	}

	// 相同 CJK 步骤重复 6 次应检出。
	steps := make([]ToolStep, 6)
	for i := range steps {
		steps[i] = step
	}
	if _, count, detected := DetectLoop(steps, 0, 0); !detected || count != 6 {
		t.Fatalf("CJK loop: detected=%v count=%d, want true/6", detected, count)
	}

	// 仅末尾输出多一个 CJK 字符，签名改变 → 不检出。
	steps[5].Output += "啊"
	if _, _, detected := DetectLoop(steps, 0, 0); detected {
		t.Fatal("differing CJK output must break the loop signature")
	}
}

func TestDetectLoopDoesNotMutateInput(t *testing.T) {
	steps := repeatedSteps(6)
	original := append([]ToolStep(nil), steps...)
	_, _, _ = DetectLoop(steps, 0, 0)
	if !reflect.DeepEqual(steps, original) {
		t.Fatal("DetectLoop must not mutate its input slice")
	}
}

func TestDetectLoopEmptyAndClamping(t *testing.T) {
	// 空输入不检出。
	if sig, count, detected := DetectLoop(nil, 0, 0); detected || count != 0 || sig != "" {
		t.Fatalf("nil steps: got (%q, %d, %v), want empty", sig, count, detected)
	}
	if _, _, detected := DetectLoop([]ToolStep{}, 0, 0); detected {
		t.Fatal("empty steps must not be detected")
	}

	// window/threshold 的非正数走默认值；用同样的正参数结果应一致。
	steps := repeatedSteps(6)
	sigA, countA, detA := DetectLoop(steps, 0, 0)
	sigB, countB, detB := DetectLoop(steps, -1, -1)
	if sigA != sigB || countA != countB || detA != detB {
		t.Fatal("non-positive window/threshold must clamp to defaults")
	}
}

func TestDetectLoopConcurrentStable(t *testing.T) {
	steps := repeatedSteps(6)
	wantSig, wantCount, wantDetected := DetectLoop(steps, 0, 0)

	const workers = 64
	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sig, count, detected := DetectLoop(steps, 0, 0)
			if sig != wantSig || count != wantCount || detected != wantDetected {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("%d concurrent calls returned inconsistent results", n)
	}
}

func TestSetLoopDetectionDefaultTrue(t *testing.T) {
	// 本文件所有会切换开关的测试都在 Cleanup 中还原 true，
	// 因此其他测试运行前后该开关都保持包初始化时的默认开启状态。
	if !LoopDetectionEnabled() {
		t.Fatal("loop detection must be enabled by default")
	}
}

func TestSetLoopDetectionToggle(t *testing.T) {
	t.Cleanup(func() { SetLoopDetection(true) })

	SetLoopDetection(true)
	if !LoopDetectionEnabled() {
		t.Fatal("SetLoopDetection(true) should enable the switch")
	}
	SetLoopDetection(false)
	if LoopDetectionEnabled() {
		t.Fatal("SetLoopDetection(false) should disable the switch")
	}
}

func TestSetLoopDetectionConcurrentSafe(t *testing.T) {
	t.Cleanup(func() { SetLoopDetection(true) })

	const workers = 8
	const rounds = 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				SetLoopDetection((i+w)%2 == 0)
				_ = LoopDetectionEnabled()
			}
		}(w)
	}
	wg.Wait()
}
