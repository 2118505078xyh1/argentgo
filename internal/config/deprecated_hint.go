package config

// deprecatedHintTable 保存已知废弃配置键的中文迁移建议。
// 每条建议都明确告诉用户“改成什么或删除什么”，而不是笼统地说
// “已忽略”。键名与 unsupportedKeys 中的拼写保持一致，按
// ASCII 精确匹配；用户传入的键即使含 CJK 字符也不会被截断或误匹配。
var deprecatedHintTable = map[string]string{
	"logLevel":       "已废弃：配置不再支持日志级别，请删除该项；当前没有对应的替代配置键。",
	"subagent_depth": "已废弃：子代理深度不再是配置项，请删除该项；运行时由 agent 的 MaxDepth 控制（默认 2）。",
	"layout":         "已废弃：终端界面不再支持自定义布局，请删除该项；界面使用默认布局。",
	"server":         "已废弃：请改用命令行参数 --serve / --rpc 及 --serve-address、--serve-port、--serve-token 配置服务，并删除该项。",
}

// maxDeprecatedHintKeyLen 限制参与查表的键长（按字节）。超过该长度的键
// 一律视为未知，避免超长输入造成无界的哈希开销；该判断不切分字符串，
// 因此不会破坏 CJK 字符的 UTF-8 编码。
const maxDeprecatedHintKeyLen = 128

// deprecatedHint 返回废弃键 key 的中文迁移建议。
//
// 对 deprecatedHintTable 中登记的已知废弃键，返回对应的中文建议，
// 说明应改成什么或删除什么；对空键、超长键、未知键（包括含 CJK
// 字符的键、大小写不同的拼写以及近似拼写）一律返回空串。
//
// 该函数是纯函数：不修改任何状态，不读取环境或磁盘，相同输入始终
// 得到相同输出。返回值来自固定表，长度有界且为完整 UTF-8（CJK 安全）。
func deprecatedHint(key string) string {
	if len(key) == 0 || len(key) > maxDeprecatedHintKeyLen {
		return ""
	}
	return deprecatedHintTable[key]
}
