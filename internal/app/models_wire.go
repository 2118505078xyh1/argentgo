package app

import (
	"example.com/argentgo/internal/cli"
	"example.com/argentgo/internal/provider"
	"example.com/argentgo/internal/tools"
)

// 本文件把 CLI 已有的 provider 模板表接成模型可调用的 `models` 工具。
// 数据源是纯静态的 provider.List()（不联网），只取公开字段转成 tools.ModelInfo，
// API key 及其环境变量名一律不进入工具结果。显式注册，默认清单逐字节不变。

// modelsToolsEnabled 默认开启；ARGENT_MODELS_TOOL=0 紧急关闭。
func modelsToolsEnabled(env func(string) string) bool {
	return env == nil || trimEnv(env("ARGENT_MODELS_TOOL")) != "0"
}

func safeModelInfos() []tools.ModelInfo {
	templates := provider.List()
	out := make([]tools.ModelInfo, 0, len(templates))
	for _, t := range templates {
		out = append(out, tools.ModelInfo{
			Name:         t.Name,
			Protocol:     string(t.Protocol),
			BaseURL:      t.BaseURL,
			DefaultModel: t.DefaultModel,
		})
	}
	return out
}

// wireModels 为本次运行的全部工具注册表安装 models 工具。
func wireModels(stdio cli.IO, set *toolset) {
	if !modelsToolsEnabled(stdio.Env) || set == nil || set.base == nil {
		return
	}
	models := safeModelInfos()
	for _, reg := range set.base.regs {
		tools.RegisterModels(reg, models)
	}
}
