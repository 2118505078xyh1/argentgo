package i18n

import (
	"fmt"
	"sort"
)

// CLI 错误修复建议消息键。这些常量定义了 ErrorHintMessage 可识别的完整键名
// 集合，与 catalog 的 UI key、错误码表、opt-in / 可靠性消息表分开维护：
// 错误建议消息描述 CLI 错误信息按失败分支归类后的修复建议，由调用方按需取用，
// 各自演进、互不干扰。键名与 internal/cli/cli_error_hint.go 的四类分支一一对应。
const (
	// ErrorHintTLS 对应 TLS / 证书校验失败分支。
	ErrorHintTLS = "errorhint.tls"
	// ErrorHintFile 对应文件不存在分支。
	ErrorHintFile = "errorhint.file"
	// ErrorHintInteractive 对应非交互环境分支。
	ErrorHintInteractive = "errorhint.interactive"
	// ErrorHintCredentials 对应缺少或无效凭证分支。
	ErrorHintCredentials = "errorhint.credentials"
)

// errorHintMessageEntry 是单个错误建议消息键在两种语言环境下的文案。
type errorHintMessageEntry struct {
	en string
	zh string
}

// errorHintMessageTable 把错误建议消息键映射到本地化文案。
//
// 中文文案与 internal/cli/cli_error_hint.go 当前的简体中文建议逐字节一致，
// 使接线后 zh-CN 输出保持字节不变；英文文案为自然对译。表中不嵌入任何输入
// 内容，长度有界。每种语言的格式动词刻意保持一致，使同一组 args 在两种语言下
// 都能正确渲染。该表独立于 catalog 与 errorMessage，不参与现有文案审计，
// 也不改变任何既有文案字节。
var errorHintMessageTable = map[string]errorHintMessageEntry{
	ErrorHintTLS: {
		"TLS/certificate verification failed: check that the system time is correct, that the CA root certificates are complete, or that a proxy/corporate gateway is not replacing the certificate; retry after confirming",
		"TLS/证书校验失败：请检查系统时间是否正确、CA 根证书是否齐全，或代理/企业网关是否替换了证书；确认后重试",
	},
	ErrorHintFile: {
		"file not found: check the path spelling and the current working directory, confirm the file has been created, and retry with an absolute path if needed",
		"文件不存在：请检查路径拼写与当前工作目录，确认文件已创建，必要时改用绝对路径后重试",
	},
	ErrorHintInteractive: {
		"non-interactive environment: provide input through explicit flags such as --yes or command-line arguments, to avoid blocking when the terminal cannot be read",
		"当前为非交互环境：请改用 --yes 等显式标志或命令行参数提供输入，避免在无法读取终端时阻塞",
	},
	ErrorHintCredentials: {
		"missing or invalid credentials: run argent login to sign in again, or configure a correct API key and retry",
		"缺少或无效的凭证：请运行 argent login 重新登录，或配置正确的 API Key 后重试",
	},
}

// missingErrorHintMessage 为未知键构造稳定的占位串。占位串包含原始键，
// 使尚未翻译的消息在 UI 中可见而非渲染为空白；同一键总是得到同一串。
// 使用字节拼接而非格式化，避免对键内容做任何解释，也不会 panic。
func missingErrorHintMessage(key string) string {
	return "<missing error hint message: " + key + ">"
}

// ErrorHintMessage 返回错误建议消息键在给定语言环境下的本地化文案。
//
// lang 经 Normalize 解析，因此 "zh"、"zh_cn"、"中文" 等别名与 "ZH-CN"
// 等大小写变体都得到中文文案；未知或空的语言环境回退到英文。提供 args
// 时用 fmt.Sprintf 格式化模板，未提供时原样返回模板。
//
// key 未命中映射表时返回一个包含该键的稳定占位串，而非 panic 或返回空串。
// 查找与返回都是整串复制，不做逐字节切分，所以含 CJK 的文案与键始终
// 保持完整的 UTF-8 字节序列。
//
// 它是纯函数：不读取配置或全局状态，不向 Missing 记录任何内容，也不修改
// 传入的 args。
func ErrorHintMessage(lang, key string, args ...any) string {
	entry, ok := errorHintMessageTable[key]
	if !ok {
		return missingErrorHintMessage(key)
	}
	template := entry.en
	if Normalize(lang) == ZhCN {
		template = entry.zh
	}
	if len(args) > 0 {
		return fmt.Sprintf(template, args...)
	}
	return template
}

// ErrorHintMessageKeys 返回所有错误建议消息键，按字典序稳定排序。
//
// 每次调用都从表中重新构造并排序，返回的是独立切片：调用方修改结果不会
// 影响表或后续调用。供审计遍历全部已定义键使用。
func ErrorHintMessageKeys() []string {
	keys := make([]string, 0, len(errorHintMessageTable))
	for key := range errorHintMessageTable {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
