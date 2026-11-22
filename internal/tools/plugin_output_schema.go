package tools

import (
	"os"
	"strings"
)

// pluginOutputSchemaEnv 是插件工具输出 schema 校验的独立门控。
const pluginOutputSchemaEnv = "ARGENT_PLUGIN_OUTPUT_SCHEMA"

func pluginOutputSchemaEnabled() bool {
	return strings.TrimSpace(os.Getenv(pluginOutputSchemaEnv)) == "1"
}
