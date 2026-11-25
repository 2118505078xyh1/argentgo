package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"example.com/argentgo/internal/config"
)

// readinessProbe 探测单个组件，返回是否就绪以及未就绪时的简短原因。
// 探测函数只在被调用时才执行 I/O，因此未开启 opt-in 时不会产生任何开销。
type readinessProbe func(ctx context.Context) (ready bool, reason string)

// readinessProbes 汇总三项本地探测。各字段均为函数，便于测试时注入桩。
type readinessProbes struct {
	config    readinessProbe
	workspace readinessProbe
	provider  readinessProbe
}

// checks 依次执行三项探测，按 config、workspace、provider 的固定顺序构造
// readinessCheck 清单。任一字段为 nil 时跳过该项，使测试可只覆盖关注的组件。
func (p readinessProbes) checks(ctx context.Context) []readinessCheck {
	items := []struct {
		name  string
		probe readinessProbe
	}{
		{readinessComponentConfig, p.config},
		{readinessComponentWorkspace, p.workspace},
		{readinessComponentProvider, p.provider},
	}
	out := make([]readinessCheck, 0, len(items))
	for _, item := range items {
		if item.probe == nil {
			continue
		}
		ready, reason := item.probe(ctx)
		out = append(out, readinessCheck{name: item.name, ready: ready, reason: reason})
	}
	return out
}

// appReadinessProbes 用真实依赖构造探测函数。返回的只是闭包，不执行任何 I/O；
// 真正的 config.Load、os.Stat 与 checkProvider 只在 checks 被调用时发生。
func (a App) appReadinessProbes(opts *Options) readinessProbes {
	return readinessProbes{
		config: func(context.Context) (bool, string) {
			root, err := workspaceRoot(a, opts.Cwd)
			if err == nil {
				_, err = config.Load(root, a.env)
			}
			if err != nil {
				return false, "配置加载失败"
			}
			return true, ""
		},
		workspace: func(context.Context) (bool, string) {
			root, err := workspaceRoot(a, opts.Cwd)
			if err != nil {
				return false, "工作区不可访问"
			}
			info, err := os.Stat(root)
			if err != nil {
				return false, "工作区不可访问"
			}
			if !info.IsDir() {
				return false, "工作区不是目录"
			}
			return true, ""
		},
		provider: func(ctx context.Context) (bool, string) {
			baseURL := firstNonEmpty(opts.BaseURL, a.env("ARGENT_BASE_URL"), a.env("OPENAI_BASE_URL"))
			if baseURL == "" {
				return false, "未配置 base URL"
			}
			check := checkProvider(ctx, baseURL)
			if check.status == statusOK {
				return true, ""
			}
			// checkProvider 的 detail 可能内嵌凭证，沿用 doctor 的脱敏逻辑，
			// 避免把凭证写进 stderr 的就绪摘要。
			reason := redactDoctorDetail(check.detail)
			if reason == "" {
				reason = "提供方不可用"
			}
			return false, reason
		},
	}
}

// readinessChecksBuilder 惰性构造就绪清单；仅在 opt-in 开启时由
// writeServeReadiness 调用。
type readinessChecksBuilder func(ctx context.Context) []readinessCheck

// writeServeReadiness 在 enabled 为真时执行一次就绪探测，并把聚合摘要作为单独
// 一行写入 w。enabled 为假（默认）时立即返回：既不构造清单也不输出任何字节，
// 从而保证 --serve 的默认 stderr 逐字节不变。
func writeServeReadiness(w io.Writer, ctx context.Context, enabled bool, build readinessChecksBuilder) {
	if !enabled {
		return
	}
	fmt.Fprintln(w, aggregateReadiness(build(ctx)).String())
}
