package server

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// healthReport 是 healthReportJSON 生成的稳定 JSON schema。四个字段都不带
// omitempty，因此无论输入是否为空，脚本总能读到同名同类型的字段。encoding/json
// 会把 map 的键按字典序输出，从而让相同输入产生逐字节一致的报告。
type healthReport struct {
	Status   string          `json:"status"`
	Version  string          `json:"version"`
	Platform string          `json:"platform"`
	Checks   map[string]bool `json:"checks"`
}

// healthReportJSON 生成可被脚本消费的结构化健康报告，供 doctor / health
// 等诊断入口复用。
//
// 设计要点：
//   - 字段稳定：status / version / platform / checks 始终存在且顺序固定，
//     checks 为 nil 或空时输出 {} 而不是 null；
//   - 敏感脱敏：所有字符串输入（含 checks 的键）都先经 security.Redact，
//     避免具有凭据形状的片段随诊断输出泄露；
//   - CJK 安全：使用 SetEscapeHTML(false)，CJK 与 <、>、& 等字符都按 UTF-8
//     原样保留，不产生 \uXXXX 转义；
//   - 纯函数：不访问网络、不读全局状态，相同输入永远得到相同字节。
//
// status / version / platform 允许为空字符串，空值原样保留为空字段，不做猜测。
func healthReportJSON(status, version, platform string, checks map[string]bool) []byte {
	// 先把键排序再脱敏写入：即使两个不同的键脱敏后碰撞成同一个键，
	// 胜出者也由固定的字典序决定，输出仍保持逐字节确定。
	names := make([]string, 0, len(checks))
	for name := range checks {
		names = append(names, name)
	}
	sort.Strings(names)

	safeChecks := make(map[string]bool, len(checks))
	for _, name := range names {
		safeChecks[security.Redact(name)] = checks[name]
	}

	report := healthReport{
		Status:   security.Redact(status),
		Version:  security.Redact(version),
		Platform: security.Redact(platform),
		Checks:   safeChecks,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(report)
	// Encode 会在末尾追加换行；报告本身不含换行，调用方需要时可自行添加。
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}
