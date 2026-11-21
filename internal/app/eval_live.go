package app

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/eval"
)

// 本文件是 R24 真模型跑批的显式 live 入口。它与 app/eval.go 的 RunSuite 刻意分离：
// RunSuite 走 ARGENT_EVAL_LIVE 门禁，默认断网；RunSuiteLive 由 CLI 的 --live 前缀
// 分派，强制使用真实 provider，不修改已有的 runScenario / runOne 路径。

const (
	// defaultLiveSuiteTimeout 是整个 live 套件的默认总超时。单场景自身的 MaxTurns
	// 仍由 agent 循环约束；这里的总超时兜住真实网络请求可能造成的无限等待。
	defaultLiveSuiteTimeout = 300 * time.Second
	// envLiveSuiteTimeout 允许按秒覆盖总超时（正整数秒，非法值回退到默认值）。
	envLiveSuiteTimeout = "ARGENT_EVAL_LIVE_TIMEOUT_SECONDS"
)

var _ cli.LiveEvalSuiteRunner = (*evaler)(nil)

// RunSuiteLive 用真实模型执行一份场景文档，是 --live 前缀的带外入口。
//
// 与 RunSuite 的差异：
//   - 不读 ARGENT_EVAL_LIVE 门禁：--live 本身已表达"真实评测"的意图，
//     凭据从 Resolve（flag/config/login/provider env）取得，无 key 时明确报错，
//     绝不静默降级到脚本化 provider。
//   - 对场景做 live-safe 归零：脚本序列相关的期望（工具序列、轮数、停止状态、
//     文本子串、请求子串）对真模型没有意义，归零后只保留 Check 的客观判据
//     （Exec + FileEquals/FileContains/FilesExist）。
//   - 打开 eval.Options.Live，在报告末尾追加完成率、token 与轮次指标。
//
// API key 只在内存中经 Resolve → LiveChatterAdapter 流向 provider；本函数及
// eval.Run 的报告路径都不会把它写入 out。
func (e *evaler) RunSuiteLive(ctx context.Context, path string, out io.Writer) (int, int, error) {
	// 强制 live 的凭据解析。Resolve 合并 flag、配置、登录态与 provider 环境变量；
	// 错误文本固定，不拼接 key。
	resolved, err := Resolve(e.stdio, cli.Options{Cwd: e.cwd})
	if err != nil {
		return 0, 0, errors.New("eval live mode: resolve failed")
	}
	if strings.TrimSpace(resolved.APIKey) == "" {
		return 0, 0, errors.New("eval live mode requires an API key (flag/config/login/provider env)")
	}

	// 协议校验复用 eval.LiveConfig.Validate 的别名白名单。ARGENT_EVAL_LIVE 未设时
	// ReadLiveConfig 返回零值配置（门禁关闭），而 --live 不需要该环境变量；因此把
	// Resolve 的结果填回 LiveConfig 再校验。若门禁已开但环境里没有 key，
	// ReadLiveConfig 会返回 ErrLiveRequiresKey——这里忽略它，因为 live 的 key 以
	// Resolve 为准（可能来自 flag/config/login，而非 ARGENT_API_KEY 环境变量）。
	liveCfg, readErr := eval.ReadLiveConfig(e.stdio.Env)
	if readErr != nil && !errors.Is(readErr, eval.ErrLiveRequiresKey) {
		return 0, 0, readErr
	}
	liveCfg.Enabled = true
	liveCfg.Protocol = resolved.Protocol
	liveCfg.Model = resolved.Model
	liveCfg.BaseURL = resolved.BaseURL
	liveCfg.APIKey = resolved.APIKey
	if err := liveCfg.Validate(); err != nil {
		return 0, 0, err
	}

	scenarios, err := eval.Load(path)
	if err != nil {
		return 0, 0, err
	}
	scenarios = liveSafeScenarios(scenarios)

	opts := eval.Options{
		ChatterFactory: eval.LiveChatterAdapter(resolved.Chatter),
		Out:            out,
		Live:           true,
	}
	// runScenario 只在 e.live 为真时安装 EventUsage 采集器。这里用一份 live 副本
	// 作为 RunFunc：既不改动 e/runScenario，也不会污染 e.live（默认 RunSuite 路径
	// 因此完全不受影响），同时保证 live 报告的 token 汇总非零。
	run := (&evaler{stdio: e.stdio, cwd: e.cwd, live: true}).runScenario

	runCtx, cancel := context.WithTimeout(ctx, liveSuiteTimeout(e.stdio.Env))
	defer cancel()
	report, err := eval.Run(runCtx, scenarios, run, opts)
	return report.Passed, report.Failed, err
}

// liveSafeScenarios 把脚本序列相关的期望清零，只留下能客观判定的 Check。
//
//   - Expect.ToolCalls / Turns / Stopped / TextContains 是脚本化回放的精确断言：
//     真模型的工具序列、轮数、是否提前停止、收尾措辞都不可预测，保留会造成假阴性。
//   - ScriptTurn.ExpectRequestContains 属于 Provider 脚本；Provider 整体清空后，
//     这些请求子串期望自然失效（ChatterFactory 注入的真实 provider 不会读取脚本）。
//   - Check（Exec / FileEquals / FileContains / FilesExist）逐字保留：它检查的是
//     工作区的真实副作用，与模型怎么措辞、按什么顺序调用工具无关。
//
// 返回新切片并逐项复制，避免改动 eval.Load 返回的场景（调用方可能复用）。
func liveSafeScenarios(scenarios []eval.Scenario) []eval.Scenario {
	safe := make([]eval.Scenario, len(scenarios))
	for i, scenario := range scenarios {
		scenario.Expect.ToolCalls = nil
		scenario.Expect.Turns = nil
		scenario.Expect.Stopped = nil
		scenario.Expect.TextContains = nil
		scenario.Provider = nil
		safe[i] = scenario
	}
	return safe
}

// liveSuiteTimeout 读取可配置的总超时秒数；非法或缺失的值回退到默认 300s。
func liveSuiteTimeout(env func(string) string) time.Duration {
	if env != nil {
		if raw := strings.TrimSpace(env(envLiveSuiteTimeout)); raw != "" {
			if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return defaultLiveSuiteTimeout
}
