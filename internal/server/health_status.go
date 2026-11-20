package server

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// 健康状态的语义化取值。客户端拿到 HealthStatus 后可以直接比较这些常量，
// 无需再自己解读裸 HTTP 状态码与响应体文本。
const (
	HealthOK        = "ok"        // 服务正常
	HealthClosing   = "closing"   // 服务正在优雅关停（非故障）
	HealthUnhealthy = "unhealthy" // 服务真的坏了
)

// healthShutdownHint 是 health 处理器在关停时写入 error 字段的固定短语。
// 用它区分“正在关停”与“其它原因导致的 503”，后者属于不健康。
const healthShutdownHint = "shutting down"

// HealthStatus 是对一次健康探针响应的语义化解读结果。
// Status 取值为 HealthOK、HealthClosing 或 HealthUnhealthy；
// Version 是响应中携带的版本号，缺失或解析失败时为空字符串。
type HealthStatus struct {
	Status  string
	Version string
}

// parseHealthResponse 把一次健康探针的状态码与响应体解读成 HealthStatus。
//
// 判定规则：
//   - 200 → HealthOK
//   - 503 且响应体含 "shutting down" → HealthClosing
//   - 其余 → HealthUnhealthy
//
// 该函数是纯函数：不访问网络、不读全局状态，相同输入永远得到相同输出。
// version 字段通过 JSON 解析提取；JSON 非法或字段缺失时仅让版本留空，
// 不影响按状态码作出的状态判定（非法 JSON 回退）。对响应体只做字节子串
// 匹配与 UTF-8 安全的 JSON 解码，不会在中文等多字节字符中间截断（CJK 安全）。
func parseHealthResponse(statusCode int, body []byte) HealthStatus {
	result := HealthStatus{Status: HealthUnhealthy}

	switch {
	case statusCode == http.StatusOK:
		result.Status = HealthOK
	case statusCode == http.StatusServiceUnavailable && bytes.Contains(body, []byte(healthShutdownHint)):
		result.Status = HealthClosing
	}

	// 仅提取 version；未知字段被忽略，解析失败时保持空字符串。
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		result.Version = payload.Version
	}
	return result
}
