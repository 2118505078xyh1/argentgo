package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// TestVerifyOutcomeHelperProcess 仅作为子进程退出码夹具：正常测试运行中立即返回，
// 只有被 verifyOutcomeTestExitError 以特定环境变量拉起时才按指定码退出。
func TestVerifyOutcomeHelperProcess(t *testing.T) {
	if os.Getenv("ARGENT_VERIFY_OUTCOME_HELPER") != "1" {
		return
	}
	code, err := strconv.Atoi(os.Getenv("ARGENT_VERIFY_OUTCOME_EXIT"))
	if err != nil {
		os.Exit(90)
	}
	os.Exit(code)
}

// verifyOutcomeTestExitError 派生一个以 code 退出的测试子进程，返回真实的 *exec.ExitError。
// 不依赖 shell，也不依赖具体的平台工具。
func verifyOutcomeTestExitError(t *testing.T, code int) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestVerifyOutcomeHelperProcess$")
	cmd.Env = append(os.Environ(),
		"ARGENT_VERIFY_OUTCOME_HELPER=1",
		fmt.Sprintf("ARGENT_VERIFY_OUTCOME_EXIT=%d", code),
	)
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper process error = %T %v, want *exec.ExitError", err, err)
	}
	return err
}

// TestClassifyVerifyOutcome 覆盖三种失败类别与 unknown 回退，以及各输入之间的优先顺序。
func TestClassifyVerifyOutcome(t *testing.T) {
	exitErr := verifyOutcomeTestExitError(t, 7)

	tests := []struct {
		name     string
		err      error
		timedOut bool
		exitCode int
		want     string
	}{
		{"nil error is unknown", nil, false, 0, verifyOutcomeUnknown},
		{"nil error with timeout flag", nil, true, -1, verifyOutcomeTimeout},
		{"plain error without exit code is unknown", errors.New("boom"), false, -1, verifyOutcomeUnknown},
		{"plain error with negative exit code is unknown", errors.New("boom"), false, -2, verifyOutcomeUnknown},
		{"plain error with zero exit code is unknown", errors.New("boom"), false, 0, verifyOutcomeUnknown},
		{"plain error with exit code one is code", errors.New("boom"), false, 1, verifyOutcomeCode},
		{"plain error with exit code two is code", errors.New("boom"), false, 2, verifyOutcomeCode},
		{"exit error is code", exitErr, false, 7, verifyOutcomeCode},
		{"exit error ignores negative exit code", exitErr, false, -1, verifyOutcomeCode},
		{"timeout flag beats exit error", exitErr, true, 7, verifyOutcomeTimeout},
		{"timeout flag beats plain error", errors.New("boom"), true, 1, verifyOutcomeTimeout},
		{"deadline sentinel is timeout", context.DeadlineExceeded, false, -1, verifyOutcomeTimeout},
		{"wrapped deadline sentinel is timeout", fmt.Errorf("run: %w", context.DeadlineExceeded), false, -1, verifyOutcomeTimeout},
		{"exec not found sentinel is infra", exec.ErrNotFound, false, -1, verifyOutcomeInfra},
		{"wrapped exec not found sentinel is infra", fmt.Errorf("lookpath: %w", exec.ErrNotFound), false, -1, verifyOutcomeInfra},
		{"lookpath error is infra", &exec.Error{Name: "go", Err: exec.ErrNotFound}, false, -1, verifyOutcomeInfra},
		{"path error permission is infra", &os.PathError{Op: "fork/exec", Path: "tools/check.exe", Err: os.ErrPermission}, false, -1, verifyOutcomeInfra},
		{"path error not exist is infra", &os.PathError{Op: "fork/exec", Path: "tools/check.exe", Err: os.ErrNotExist}, false, -1, verifyOutcomeInfra},
		{"wrapped path error is infra", fmt.Errorf("start: %w", &os.PathError{Op: "fork/exec", Path: "x", Err: os.ErrPermission}), false, -1, verifyOutcomeInfra},
		{"cancelled context is infra", context.Canceled, false, -1, verifyOutcomeInfra},
		{"wrapped cancelled context is infra", fmt.Errorf("run: %w", context.Canceled), false, -1, verifyOutcomeInfra},
		{"spawn failure wins over positive exit code", &exec.Error{Name: "go", Err: exec.ErrNotFound}, false, 1, verifyOutcomeInfra},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyVerifyOutcome(tc.err, tc.timedOut, tc.exitCode); got != tc.want {
				t.Fatalf("classifyVerifyOutcome(%v, %v, %d) = %q, want %q", tc.err, tc.timedOut, tc.exitCode, got, tc.want)
			}
		})
	}
}

// TestClassifyVerifyOutcomeExitCodeBoundaries 锁定退出码的边界处理：只有正数代表代码错误，
// 零和所有负数（含极值）在没有可识别错误类型时都回落到 unknown。
func TestClassifyVerifyOutcomeExitCodeBoundaries(t *testing.T) {
	err := errors.New("verify failure")
	tests := []struct {
		exitCode int
		want     string
	}{
		{math.MinInt, verifyOutcomeUnknown},
		{-2, verifyOutcomeUnknown},
		{-1, verifyOutcomeUnknown},
		{0, verifyOutcomeUnknown},
		{1, verifyOutcomeCode},
		{2, verifyOutcomeCode},
		{math.MaxInt, verifyOutcomeCode},
	}
	for _, tc := range tests {
		if got := classifyVerifyOutcome(err, false, tc.exitCode); got != tc.want {
			t.Errorf("exitCode = %d: got %q, want %q", tc.exitCode, got, tc.want)
		}
	}
}

// TestClassifyVerifyOutcomeIgnoresErrorText 确保分类不读取错误文本：即便消息里写着
// "超时"或 "timed out"，只要没有对应的哨兵/标志/退出码，就不能被误判为超时。
// 这也是 CJK 无关性的核心断言。
func TestClassifyVerifyOutcomeIgnoresErrorText(t *testing.T) {
	for _, msg := range []string{"超时", "timed out", "fork/exec failed", "验证基础设施故障", "exit status 1", "代码错误"} {
		if got := classifyVerifyOutcome(errors.New(msg), false, -1); got != verifyOutcomeUnknown {
			t.Errorf("message %q: got %q, want %q", msg, got, verifyOutcomeUnknown)
		}
	}
}

// TestClassifyVerifyOutcomeIsCJKIndependentAndStable 验证多语言错误文本不会改变结构化输入的结果，
// 且重复调用稳定返回同一分类。
func TestClassifyVerifyOutcomeIsCJKIndependentAndStable(t *testing.T) {
	tests := []struct {
		err      error
		timedOut bool
		exitCode int
		want     string
	}{
		{errors.New("boom"), false, 1, verifyOutcomeCode},
		{errors.New("验证失败：编译错误"), false, 1, verifyOutcomeCode},
		{errors.New("💥 失败"), false, 1, verifyOutcomeCode},
		{fmt.Errorf("包装: %w", exec.ErrNotFound), false, -1, verifyOutcomeInfra},
		{fmt.Errorf("已超时: %w", context.DeadlineExceeded), false, -1, verifyOutcomeTimeout},
		{errors.New("基础设施故障"), true, -1, verifyOutcomeTimeout},
	}
	for _, tc := range tests {
		for i := 0; i < 3; i++ {
			if got := classifyVerifyOutcome(tc.err, tc.timedOut, tc.exitCode); got != tc.want {
				t.Fatalf("call %d with %v: got %q, want %q", i, tc.err, got, tc.want)
			}
		}
	}
}

// TestVerifyOutcomeTag 锁定分类到展示标记的映射：只有三类明确失败产生标记，
// unknown 与无法识别的值一律返回空串，保证接线后默认输出不变。
func TestVerifyOutcomeTag(t *testing.T) {
	tests := []struct {
		class string
		want  string
	}{
		{verifyOutcomeCode, "[verify-outcome: code]"},
		{verifyOutcomeInfra, "[verify-outcome: infra]"},
		{verifyOutcomeTimeout, "[verify-outcome: timeout]"},
		{verifyOutcomeUnknown, ""},
		{"", ""},
		{"Code", ""},
	}
	for _, tc := range tests {
		if got := verifyOutcomeTag(tc.class); got != tc.want {
			t.Errorf("verifyOutcomeTag(%q) = %q, want %q", tc.class, got, tc.want)
		}
	}
}

// TestVerifyOutcomeEnabledIsOptIn 确保分类标记默认关闭，只有 ARGENT_VERIFY_OUTCOME=1 才开启。
func TestVerifyOutcomeEnabledIsOptIn(t *testing.T) {
	if verifyOutcomeEnabled(nil) {
		t.Fatal("env 为 nil 时必须默认关闭")
	}
	if verifyOutcomeEnabled(envFrom(nil)) {
		t.Fatal("未设置 ARGENT_VERIFY_OUTCOME 时必须默认关闭")
	}
	for _, off := range []string{"", "0", "yes", "true", " true "} {
		if verifyOutcomeEnabled(envFrom(map[string]string{"ARGENT_VERIFY_OUTCOME": off})) {
			t.Errorf("ARGENT_VERIFY_OUTCOME=%q 不应开启", off)
		}
	}
	for _, on := range []string{"1", " 1 "} {
		if !verifyOutcomeEnabled(envFrom(map[string]string{"ARGENT_VERIFY_OUTCOME": on})) {
			t.Errorf("ARGENT_VERIFY_OUTCOME=%q 应开启", on)
		}
	}
}
