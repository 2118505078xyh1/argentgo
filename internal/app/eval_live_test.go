package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/eval"
)

// 本文件端到端验证 --live 的显式入口 RunSuiteLive：它复用 eval_live_local_test.go
// 的 httptest 假服务端与协议脚本，让真实的三协议 llm.Chatter 驱动 eval.Run +
// 一份 live 副本的 runScenario。断言覆盖：F 过 N 挂、完成率与 Usage 汇总、
// 缺 key 明确报错、ARGENT_EVAL_LIVE 未设仍 live、报告不含 key、默认路径不受影响。

// liveScenarioEnv 构造指向假服务端的解析环境。gate 参数决定 ARGENT_EVAL_LIVE：
// RunSuiteLive 应忽略该门禁，而默认 RunSuite 只在它为 "1" 时才走 live。
func liveScenarioEnv(srvURL, key, gate string) func(string) string {
	values := map[string]string{
		"ARGENT_API_KEY":                   key,
		"ARGENT_PROTOCOL":                  "chat",
		"ARGENT_BASE_URL":                  srvURL + liveLocalBaseURL,
		"ARGENT_MODEL":                     "test-model",
		"ARGENT_EVAL_LIVE":                 gate,
		"ARGENT_EVAL_LIVE_TIMEOUT_SECONDS": "20", // 假服务端，避免真等 300s
	}
	return func(name string) string { return values[name] }
}

// writeLiveSuiteFile 把场景编码成 eval.Load 接受的 {"scenarios": [...]} 文档。
func writeLiveSuiteFile(t *testing.T, scenarios ...eval.Scenario) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"scenarios": scenarios})
	if err != nil {
		t.Fatalf("marshal scenarios: %v", err)
	}
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write scenarios: %v", err)
	}
	return path
}

// liveTokenPrompt 从报告的 token 行解析 prompt token 数，供 Usage>0 断言。
func liveTokenPrompt(t *testing.T, report string) int {
	t.Helper()
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "# tokens: ") {
			var prompt, completion, cached, cacheWrite, total int
			_, err := fmt.Sscanf(line,
				"# tokens: prompt=%d completion=%d cached=%d cache_write=%d total=%d",
				&prompt, &completion, &cached, &cacheWrite, &total)
			if err != nil {
				t.Fatalf("parse token line %q: %v", line, err)
			}
			return prompt
		}
	}
	t.Fatalf("report has no token metrics line:\n%s", report)
	return 0
}

// TestRunSuiteLiveFixPassesNoopFails 证明端到端评分只看客观 Check：F 场景的文件内容
// 符合预期→通过，N 场景的 FileEquals 与真实内容不符→失败；报告含 0.5 完成率与非零
// token 汇总，且不泄漏 API key。ARGENT_EVAL_LIVE 为空但 --live 仍发起真实 HTTP 请求。
func TestRunSuiteLiveFixPassesNoopFails(t *testing.T) {
	p := liveLocalProtocols()[0] // chat：假服务端按序回放 read 轮 + 收尾轮
	srv, rec := newLiveLocalServer(t, p.toolScript, p.finalScript)
	const key = "live-e2e-secret-key-42"

	fix := liveLocalToolScenario(p)
	fix.Name = "live fix (chat)"
	noop := liveLocalToolScenario(p)
	noop.Name = "live noop (chat)"
	// Check 与 F 的唯一差别：期望的字节内容错误。模型收尾说什么都不影响判定。
	noop.Check = eval.Check{FileEquals: map[string]string{"note.txt": "WRONG CONTENT\n"}}
	path := writeLiveSuiteFile(t, fix, noop)

	e := NewEvaler(cli.IO{Out: io.Discard, Err: io.Discard, Env: liveScenarioEnv(srv.URL, key, "")})
	runner, ok := e.(cli.LiveEvalSuiteRunner)
	if !ok {
		t.Fatal("NewEvaler result does not implement cli.LiveEvalSuiteRunner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out bytes.Buffer
	passed, failed, err := runner.RunSuiteLive(ctx, path, &out)

	// N 场景失败，因此 eval.Run 返回非 nil 错误；报告仍然完整。
	if err == nil {
		t.Fatalf("RunSuiteLive error = nil, want failure for the noop scenario; report:\n%s", out.String())
	}
	if passed != 1 || failed != 1 {
		t.Fatalf("passed=%d failed=%d, want 1 and 1; report:\n%s", passed, failed, out.String())
	}
	report := out.String()
	for _, want := range []string{
		"1..2",
		"ok 1 - live fix (chat)",
		"not ok 2 - live noop (chat)",
		"# 1 passed, 1 failed",
		"# completion rate: 0.500",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q:\n%s", want, report)
		}
	}
	if got := liveTokenPrompt(t, report); got <= 0 {
		t.Errorf("prompt tokens = %d, want > 0 (live usage collection); report:\n%s", got, report)
	}
	if strings.Contains(report, key) {
		t.Errorf("report leaks the API key %q:\n%s", key, report)
	}
	if reqs := rec.snapshot(); len(reqs) == 0 {
		t.Error("no HTTP requests reached the fake server; --live did not use the real chatter")
	}
}

// TestRunSuiteLiveRequiresKey 证明缺 key 时明确报错，且不会加载场景或发起网络请求。
// 即便 ARGENT_EVAL_LIVE=1（门禁已开），live 仍以 Resolve 的 key 为准；这里 flag、
// 配置与 provider 环境变量都没有 key，必须报错而不是静默走脚本化。
func TestRunSuiteLiveRequiresKey(t *testing.T) {
	p := liveLocalProtocols()[0]
	srv, rec := newLiveLocalServer(t, p.finalScript)
	base := liveScenarioEnv(srv.URL, "", "1") // 无 key
	env := func(name string) string {
		if name == "OPENAI_API_KEY" {
			return ""
		}
		return base(name)
	}
	e := NewEvaler(cli.IO{Out: io.Discard, Err: io.Discard, Env: env})
	var out bytes.Buffer
	passed, failed, err := e.(*evaler).RunSuiteLive(context.Background(), "does-not-exist.json", &out)
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("error = %v, want an explicit API key error", err)
	}
	if passed != 0 || failed != 0 || out.Len() != 0 {
		t.Fatalf("passed=%d failed=%d out=%q, want zeros and no report", passed, failed, out.String())
	}
	if reqs := rec.snapshot(); len(reqs) != 0 {
		t.Errorf("made %d HTTP requests without a key", len(reqs))
	}
}

// TestRunSuiteLiveIgnoresGateAndLeavesDefaultUntouched 证明两件事：
//  1. ARGENT_EVAL_LIVE 未设时 --live 仍然真实调用 provider；
//  2. 同一个 evaler 的默认 RunSuite 始终保持脚本化，且在 --live 之后也不受影响
//     （RunSuiteLive 使用 live 副本，不污染 e.live）。
func TestRunSuiteLiveIgnoresGateAndLeavesDefaultUntouched(t *testing.T) {
	p := liveLocalProtocols()[0]
	srv, rec := newLiveLocalServer(t, p.finalScript)
	env := liveScenarioEnv(srv.URL, "live-gate-key", "") // 门禁关闭
	e := NewEvaler(cli.IO{Out: io.Discard, Err: io.Discard, Env: env})

	// 一份纯脚本场景：默认 RunSuite 应完全由脚本回放，零 HTTP 请求。
	sc := eval.Scenario{
		Name:     "scripted stays scripted",
		Prompt:   "say hello from seed",
		Provider: []eval.ScriptTurn{{Respond: eval.Respond{Text: "the note says hello from seed"}}},
	}
	path := writeLiveSuiteFile(t, sc)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var first bytes.Buffer
	passed, failed, err := e.(cli.EvalSuiteRunner).RunSuite(ctx, path, &first)
	if err != nil || passed != 1 || failed != 0 {
		t.Fatalf("default RunSuite = %d/%d/%v, want 1/0/nil:\n%s", passed, failed, err, first.String())
	}
	if reqs := rec.snapshot(); len(reqs) != 0 {
		t.Fatalf("default RunSuite made %d HTTP requests; want scripted (gate off)", len(reqs))
	}

	// 同一 env（门禁仍关）下 --live 必须真实调用。
	var liveOut bytes.Buffer
	passed, failed, err = e.(cli.LiveEvalSuiteRunner).RunSuiteLive(ctx, path, &liveOut)
	if err != nil || passed != 1 || failed != 0 {
		t.Fatalf("RunSuiteLive = %d/%d/%v, want 1/0/nil:\n%s", passed, failed, err, liveOut.String())
	}
	if reqs := rec.snapshot(); len(reqs) == 0 {
		t.Fatal("--live with ARGENT_EVAL_LIVE unset did not reach the fake server")
	}

	// --live 之后再跑默认路径：仍应零新增请求（e.live 未被污染）。
	before := len(rec.snapshot())
	var second bytes.Buffer
	passed, failed, err = e.(cli.EvalSuiteRunner).RunSuite(ctx, path, &second)
	if err != nil || passed != 1 || failed != 0 {
		t.Fatalf("second default RunSuite = %d/%d/%v:\n%s", passed, failed, err, second.String())
	}
	if after := len(rec.snapshot()); after != before {
		t.Errorf("default RunSuite after --live made %d extra HTTP requests", after-before)
	}
}
