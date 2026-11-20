package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// yamlExportHeader 把转录导出标记为 YAML 文档。开头的注释是合法的 YAML，
// 使原本是 JSON 的主体具有自描述性。
const yamlExportHeader = "# argent session export - YAML 1.2 (JSON-compatible)\n"

// jsonExportToYAML 不依赖任何第三方 YAML 库，就把会话 JSON 转录渲染为 YAML
// 文档。JSON 是 YAML 1.2 的真子集，因此 session.ExportProject 产生的美化 JSON
// 已经是合法的 YAML 文档：它保留每个字段（包括作为原始 UTF-8 的中文文本），
// 且能被 JSON 和 YAML 读取器同时解析。我们只验证负载并加上 YAML 注释标记前缀；
// 不重写任何数据。
func jsonExportToYAML(data []byte) ([]byte, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("session: invalid JSON export cannot be rendered as YAML")
	}
	out := make([]byte, 0, len(yamlExportHeader)+len(data))
	out = append(out, yamlExportHeader...)
	out = append(out, data...)
	return out, nil
}

// isYAMLExport 报告用户提供的 --format 值是否选择 YAML 导出。
// 忽略大小写和周围空白，与 session.ExportProject 处理的其他结构化格式一致。
func isYAMLExport(format string) bool {
	return strings.EqualFold(strings.TrimSpace(format), "yaml")
}
