package server

// ConfigInfo 是 config.info RPC 返回的配置摘要，供原生窗口状态栏显示
// 当前会话真实使用的 model/provider/workspace，替代硬编码的假模型名。
//
// 字段使用显式的 wire 标签，字段顺序即 JSON 键顺序，使客户端拿到稳定的
// {"model":...,"provider":...,"workspace":...} 形态。这里只放可安全披露的
// 非敏感信息；workspace 由 RPC 层在跨边界前折叠绝对路径（见 scrubAbsolutePaths），
// 因此本类型绝不承载调用方工作区的绝对路径。
type ConfigInfo struct {
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Workspace string `json:"workspace"`
}

// ConfigInfoProvider 是可注入 Config 的可选配置来源，仿 SessionTreeStore 的
// 零值语义：没有实现（或未注入）它的接线，config.info 返回干净的
// "not configured" 错误，所有既有 RPC 方法的行为逐字节不变。
type ConfigInfoProvider interface {
	ConfigInfo() ConfigInfo
}
