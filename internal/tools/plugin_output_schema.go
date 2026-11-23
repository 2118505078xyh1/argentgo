package tools

import (
	"os"
	"strings"
)

// pluginOutputSchemaEnv 是插件工具输出 schema 校验的独立门控。默认开启
// （声明了 schema 的工具必须输出合法 JSON）；仅显式设为 "0"（允许首尾
// 空白）时关闭。
const pluginOutputSchemaEnv = "ARGENT_PLUGIN_OUTPUT_SCHEMA"

func pluginOutputSchemaEnabled() bool {
	return strings.TrimSpace(os.Getenv(pluginOutputSchemaEnv)) != "0"
}
