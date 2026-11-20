package app

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// verifyEnvHasValue 报告 env 中是否有 "K=V" 条目的值恰好等于给定值。
// 测试通过 t.Setenv 设置唯一哨兵，使断言与父环境中的其他内容无关。
// （接上行）
func verifyEnvHasValue(env []string, value string) bool {
	for _, entry := range env {
		_, v, _ := strings.Cut(entry, "=")
		if v == value {
			return true
		}
	}
	return false
}

// verifyEnvHasEntry 报告是否存在确切的 "K=V" 条目。
func verifyEnvHasEntry(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

// TestVerifyEnvironmentSanitizesHostPrivateVars 锁定 P1 修复：验证子进程
// 不得继承主机的会话管道。proc.SanitizeEnv 移除确切的名称
// ARGENT_SESSION_ID / ARGENT_RUN_ID 以及前缀
// ARGENT_PARENT_* / SUBAGENT_PARENT_*；旧版的 GOFLAGS 剥离必须保留。
func TestVerifyEnvironmentSanitizesHostPrivateVars(t *testing.T) {
	const (
		sessionVal   = "vSentinel-session-9f3a"
		runVal       = "vSentinel-run-7c1b"
		parentVal    = "vSentinel-parent-2d8e"
		subParentVal = "vSentinel-subparent-4a6f"
		goflagsVal   = "-toolexec=evil"
		keepVal      = "vSentinel-keep-5b2c"
	)

	// 镜像 proc.SanitizeEnv 使用的平台大小写规则：混合大小写的
	// 私有键在 Windows 上被过滤， elsewhere 保留。
	mixedCaseKey := "argent_session_id"
	mixedCaseVal := "vSentinel-mixedcase-8e1d"

	t.Setenv("ARGENT_SESSION_ID", sessionVal)
	t.Setenv("ARGENT_RUN_ID", runVal)
	t.Setenv("ARGENT_PARENT_PROBE", parentVal)
	t.Setenv("SUBAGENT_PARENT_PROBE", subParentVal)
	t.Setenv("GOFLAGS", goflagsVal)
	t.Setenv("ARGENT_VERIFY_KEEP", keepVal)
	t.Setenv(mixedCaseKey, mixedCaseVal)

	got := verifyEnvironment()

	// 主机私有的值必须消失；GOFLAGS 仍必须被剥离。
	for _, leaked := range []string{sessionVal, runVal, parentVal, subParentVal, goflagsVal} {
		if verifyEnvHasValue(got, leaked) {
			t.Errorf("verifyEnvironment leaked private/unsafe value %q", leaked)
		}
	}
	// 混合大小写的私有键遵循与 proc 相同的平台规则：
	// 在 Windows 上过滤（不区分大小写），其他地方保留。
	if runtime.GOOS == "windows" {
		if verifyEnvHasValue(got, mixedCaseVal) {
			t.Errorf("verifyEnvironment leaked mixed-case private value %q", mixedCaseVal)
		}
	} else if !verifyEnvHasValue(got, mixedCaseVal) {
		t.Errorf("verifyEnvironment dropped the case-sensitive normal variable %q", mixedCaseKey)
	}
	// 任何写法下都没有 GOFLAGS 条目。
	for _, entry := range got {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GOFLAGS") {
			t.Errorf("verifyEnvironment leaked GOFLAGS entry %q", entry)
		}
	}

	// 合法的变量——自定义的操作人员变量加上继承的系统变量——
	// 原样透传，使验证行为不退化。
	if !verifyEnvHasEntry(got, "ARGENT_VERIFY_KEEP="+keepVal) {
		t.Errorf("verifyEnvironment dropped the normal variable ARGENT_VERIFY_KEEP")
	}
	if _, ok := os.LookupEnv("PATH"); ok && !verifyHasKey(got, "PATH") {
		t.Errorf("verifyEnvironment dropped PATH")
	}
	if len(got) == 0 {
		t.Errorf("verifyEnvironment returned an empty environment")
	}
}

// verifyHasKey 报告 env 中是否有条目具有给定键（大小写折叠，
// 以匹配 Windows 上 PATH/Path 大小写可能变化的情况）。
func verifyHasKey(env []string, key string) bool {
	for _, entry := range env {
		k, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(k, key) {
			return true
		}
	}
	return false
}

// verifyEnvProbeProgram 为它能看到的每个受监视变量打印一个标记。
// 它被构建为临时 .exe 并通过 NewVerifyFunc 启动，使测试观察到验证子进程
// 收到的*实际*环境，而不只是 verifyEnvironment() 返回的切片。
// （接上行）
const verifyEnvProbeProgram = `package main

import (
	"fmt"
	"os"
)

func main() {
	for _, k := range []string{
		"ARGENT_SESSION_ID",
		"ARGENT_RUN_ID",
		"ARGENT_PARENT_PROBE",
		"SUBAGENT_PARENT_PROBE",
		"GOFLAGS",
		"ARGENT_VERIFY_KEEP",
	} {
		if v, ok := os.LookupEnv(k); ok {
			fmt.Printf("SEEN %s=%s\n", k, v)
		}
	}
}
`

// buildVerifyEnvProbe 在工作区内构建环境探针并返回其相对 .exe 名称。
// Go 工具链不可用时跳过。
func buildVerifyEnvProbe(t *testing.T, workspace string) string {
	t.Helper()
	writeVerifyFile(t, workspace, "go.mod", "module verifyenvprobe\n\ngo 1.21\n")
	writeVerifyFile(t, workspace, "main.go", verifyEnvProbeProgram)

	const exe = "verifyenvprobe.exe"
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = workspace
	cmd.Env = verifyEnvironment() // 用相同的清理后环境构建
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Go toolchain unavailable for env probe build: %v: %s", err, out)
	}
	return exe
}

// TestVerifyChildCannotSeeHostPrivateEnv 是过滤测试的端到端对应：哨兵值被植入父进程，
// 然后一个真实的验证子进程报告它实际能看到哪些变量。
// 普通哨兵必须可见（证明环境继承有效），而每个主机私有变量和 GOFLAGS
// 都必须不出现在子进程中。
// （接上行）
func TestVerifyChildCannotSeeHostPrivateEnv(t *testing.T) {
	const (
		sessionVal   = "childSentinel-session-3a7f"
		runVal       = "childSentinel-run-1b9d"
		parentVal    = "childSentinel-parent-6c2e"
		subParentVal = "childSentinel-subparent-0e4a"
		goflagsVal   = "-toolexec=child-evil"
		keepVal      = "childSentinel-keep-8d5b"
	)

	t.Setenv("ARGENT_SESSION_ID", sessionVal)
	t.Setenv("ARGENT_RUN_ID", runVal)
	t.Setenv("ARGENT_PARENT_PROBE", parentVal)
	t.Setenv("SUBAGENT_PARENT_PROBE", subParentVal)
	t.Setenv("GOFLAGS", goflagsVal)
	t.Setenv("ARGENT_VERIFY_KEEP", keepVal)

	workspace := t.TempDir()
	exe := buildVerifyEnvProbe(t, workspace)

	verify := NewVerifyFunc([]string{exe}, 5000, workspace)
	label, passed, output, ok := verify(context.Background(), nil)
	if !ok || !passed {
		t.Fatalf("probe failed to run: (label=%q, passed=%v, output=%q, ok=%v)", label, passed, output, ok)
	}

	// 普通变量证明子进程确实继承了经过清理的环境。
	if !strings.Contains(output, "ARGENT_VERIFY_KEEP="+keepVal) {
		t.Errorf("child did not see the normal variable; env inheritance broken: %q", output)
	}
	// 任何主机私有哨兵和 GOFLAGS 值都不能到达子进程。
	for _, leaked := range []string{sessionVal, runVal, parentVal, subParentVal, goflagsVal} {
		if strings.Contains(output, leaked) {
			t.Errorf("verify child observed private/unsafe value %q: %q", leaked, output)
		}
	}
	if strings.Contains(output, "SEEN GOFLAGS=") || strings.Contains(output, "SEEN ARGENT_SESSION_ID=") ||
		strings.Contains(output, "SEEN ARGENT_RUN_ID=") || strings.Contains(output, "SEEN ARGENT_PARENT_PROBE=") ||
		strings.Contains(output, "SEEN SUBAGENT_PARENT_PROBE=") {
		t.Errorf("verify child observed a sanitized variable name: %q", output)
	}
}
