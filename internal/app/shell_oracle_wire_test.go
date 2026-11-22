package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/llm"
	"github.com/2118505078xyh1/argentgo/internal/shell"
)

// R51研究#26：安全咨询谓词在生产接线中从未被设置，内建 shell 的
// fail-closed 分支因此恒不触发。本文件锁定三件事：
//  1. app.Execute 建链后把"转调 chain"的谓词注入 shell；
//  2. interp.go 的 fail-closed 分支真的经 CurrentSafety 咨询（静态守护）；
//  3. 未注入时 CurrentSafety 默认放行，系统 shell 路径逐字节不变。
//
// 注意：这些测试会改动包级 oracle，全部用 t.Cleanup 复位为 nil，避免污染
// 同一测试二进制中的其他用例。

// TestShellOracleWire 是端到端接线回归：--yolo 关闭、配置一条针对命令的
// deny 规则、开启内建 shell 后，bash 工具调用发出的命令必须被 chain 拒绝，
// 模型能看到 "rejected by safety policy"。
//
// 关键设计：外层工具守卫与内层 oracle 调用的是同一条 chain，若两者看到
// 完全相同的命令字符串，外层守卫会先拒绝，oracle 便得不到验证机会。因此
// 模型传入带引号的命令 `echo "danger"`：
//   - 外层 chain.Check 收到原始命令 `echo "danger"`，不匹配规则 "echo danger"，
//     于是放行，工具真正运行；
//   - 内建 shell 展开后把真实 argv 交给 oracle：`echo danger`，命中规则被拒。
//
// 这同时证明 oracle 咨询的是"展开后的真实 argv"，更难被分词差分绕过。
func TestShellOracleWire(t *testing.T) {
	// 内建 shell 由进程环境变量控制（shell.builtinShellEnabled 用 os.Getenv）。
	t.Setenv("ARGENT_BUILTIN_SHELL", "1")
	t.Cleanup(func() { shell.SetSafetyOracle(nil) })

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "argent.json"),
		`{"permissions":{"rules":[{"decision":"deny","tool":"bash","command":"echo danger"}]}}`)

	env := envFrom(map[string]string{
		"HOME":                 t.TempDir(),
		"USERPROFILE":          t.TempDir(),
		"ARGENT_YOLO":          "0",
		"ARGENT_BUILTIN_SHELL": "1",
	})
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	// 外层守卫对只读命令的 Ask 决策会被 autoApprover 放行，
	// 使命令真正进入内建解释器（否则 oracle 不会被调用）。
	res.Approve = autoApprover{}
	res.Chatter = &toolCallChatter{first: llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID: "c1", Type: "function",
			Function: llm.ToolCallFunction{
				Name:      "bash",
				Arguments: `{"command":"echo \"danger\""}`,
			},
		}},
	}}

	store := testStore(t, res)
	sess, err := store.NewSession("shell-oracle-wire")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), cli.IO{Env: env}, res, sess, "run", nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !toolOutputContains(t, sess, "rejected by safety policy") {
		t.Fatal("注入的安全咨询没有拒绝展开后的命令：工具输出中缺少 rejected by safety policy")
	}
}

// TestInterpFailClosedCallsCurrentSafety 是静态守护：interp.go 的 fail-closed
// 分支必须经 CurrentSafety 判定，且不能再退回"仅检查 it.Safety != nil"的旧式
// 直接调用（那会让包级 oracle 永远不被咨询）。
func TestInterpFailClosedCallsCurrentSafety(t *testing.T) {
	data, err := os.ReadFile("../shell/interp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, `if !CurrentSafety(it.Safety, strings.Join(argv, " "))`) {
		t.Fatal("interp.go 的 fail-closed 分支没有调用 CurrentSafety(it.Safety, strings.Join(argv, \" \"))")
	}
	if strings.Contains(src, `it.Safety != nil && !it.Safety(`) {
		t.Fatal("interp.go 仍残留旧式直接调用 it.Safety，包级 oracle 会被绕过")
	}
}

// TestCurrentSafetyBaselineAndCJK 固定默认态与 CJK 透传：未注入时
// CurrentSafety(nil, ...) 必须返回 true（系统 shell 路径逐字节不变），
// 注入谓词后 CJK 命令的字节必须原样送达。
func TestCurrentSafetyBaselineAndCJK(t *testing.T) {
	t.Cleanup(func() { shell.SetSafetyOracle(nil) })
	shell.SetSafetyOracle(nil)

	const cjk = "回显 你好 世界"
	if !shell.CurrentSafety(nil, cjk) {
		t.Fatalf("未注入时 CurrentSafety(nil, %q) = false，期望 true（放行）", cjk)
	}

	shell.SetSafetyOracle(func(got string) bool { return got == cjk })
	if !shell.CurrentSafety(nil, cjk) {
		t.Fatalf("CJK 命令未被原样交给谓词：%q", cjk)
	}
	if shell.CurrentSafety(nil, "回显 你好") {
		t.Fatal("不同 CJK 命令不应被误判为放行")
	}
}
