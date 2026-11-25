package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/bash"
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/llm"
	"example.com/argentgo/internal/session"
)

// R51研究#26：安全咨询谓词在生产接线中从未被设置，内建 shell 的
// fail-closed 分支因此恒不触发。本文件锁定三件事：
//  1. app.Execute 建链后把"转调 chain"的谓词经 ctx 注入 shell；
//  2. interp.go 的 fail-closed 分支真的经 CurrentSafety 咨询（静态守护）；
//  3. 未注入时 CurrentSafety 默认放行，系统 shell 路径逐字节不变。
//
// 谓词随运行 ctx 传播（bash.WithSafetyOracle），不再有进程级全局状态：
// 并发 Execute 各自隔离，运行结束 ctx 失效即无残留。

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
	// 内建 shell 由进程环境变量控制（bash.builtinShellEnabled 用 os.Getenv）。
	t.Setenv("ARGENT_BUILTIN_SHELL", "1")

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
	data, err := os.ReadFile("../bash/interp.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	if !strings.Contains(src, `if !CurrentSafety(it.Ctx, it.Safety, strings.Join(argv, " "))`) {
		t.Fatal("interp.go 的 fail-closed 分支没有调用 CurrentSafety(it.Ctx, it.Safety, strings.Join(argv, \" \"))")
	}
	if strings.Contains(src, `it.Safety != nil && !it.Safety(`) {
		t.Fatal("interp.go 仍残留旧式直接调用 it.Safety，ctx oracle 会被绕过")
	}
}

// TestCurrentSafetyBaselineAndCJK 固定默认态与 CJK 透传：未注入时
// CurrentSafety(ctx, nil, ...) 必须返回 true（系统 shell 路径逐字节不变），
// 经 ctx 注入谓词后 CJK 命令的字节必须原样送达。
func TestCurrentSafetyBaselineAndCJK(t *testing.T) {
	const cjk = "回显 你好 世界"
	if !bash.CurrentSafety(context.Background(), nil, cjk) {
		t.Fatalf("未注入时 CurrentSafety(ctx, nil, %q) = false，期望 true（放行）", cjk)
	}

	ctx := bash.WithSafetyOracle(context.Background(), func(got string) bool { return got == cjk })
	if !bash.CurrentSafety(ctx, nil, cjk) {
		t.Fatalf("CJK 命令未被原样交给谓词：%q", cjk)
	}
	if bash.CurrentSafety(ctx, nil, "回显 你好") {
		t.Fatal("不同 CJK 命令不应被误判为放行")
	}
}

// TestConcurrentExecuteSafetyOracleIsolation 证明并发的两个 Execute 各自用
// 自己的 chain 做安全判定：两条不同的 deny 规则只拒绝各自的命令。
// 旧实现用进程级全局 oracle，后启动的运行会覆盖前者，使 A 的命令被 B 的
// chain 判定（串味）——本测试会因此失败。这里用假 chain 的等价物：每条
// 规则只认自己的命令，若串味则命令不会被对应规则拒绝。
func TestConcurrentExecuteSafetyOracleIsolation(t *testing.T) {
	// 内建 shell 由进程环境变量控制，必须在派生 goroutine 前设置。
	t.Setenv("ARGENT_BUILTIN_SHELL", "1")

	cases := []struct {
		name     string
		denyCmd  string
		modelCmd string
	}{
		{"runA", "echo danger", `echo "danger"`},
		{"runB", "echo poison", `echo "poison"`},
	}

	// 在 main goroutine 预备好每个运行的全部资源，避免在 goroutine 内调用
	// t.TempDir / t.Fatal 等不宜并发使用的测试 API。
	type prepared struct {
		name string
		io   cli.IO
		res  Resolved
		sess *session.Session
	}
	var runs []prepared
	for _, tc := range cases {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "argent.json"),
			fmt.Sprintf(`{"permissions":{"rules":[{"decision":"deny","tool":"bash","command":%q}]}}`, tc.denyCmd))
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
		// 使命令真正进入内建解释器，oracle 才有机会被咨询。
		res.Approve = autoApprover{}
		res.Chatter = &toolCallChatter{first: llm.Message{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Type: "function",
				Function: llm.ToolCallFunction{
					Name:      "bash",
					Arguments: fmt.Sprintf(`{"command":%q}`, tc.modelCmd),
				},
			}},
		}}
		store := testStore(t, res)
		sess, err := store.NewSession(tc.name)
		if err != nil {
			t.Fatal(err)
		}
		runs = append(runs, prepared{name: tc.name, io: cli.IO{Env: env}, res: res, sess: sess})
	}

	type outcome struct {
		name string
		err  error
	}
	results := make(chan outcome, len(runs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, run := range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 同时开跑，放大旧全局实现的串味窗口
			_, err := Execute(context.Background(), run.io, run.res, run.sess, "run", nil)
			results <- outcome{name: run.name, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for o := range results {
		if o.err != nil {
			t.Fatalf("%s: Execute: %v", o.name, o.err)
		}
	}
	// 每个运行必须只被自己的规则拒绝：A 的命令 "echo danger" 只命中 A 的规则，
	// B 的 "echo poison" 只命中 B 的规则。若串味，至少一条不会被拒。
	for _, run := range runs {
		if !toolOutputContains(t, run.sess, "rejected by safety policy") {
			t.Fatalf("%s: 工具输出中缺少 rejected by safety policy（安全判定可能串味）", run.name)
		}
	}
}
