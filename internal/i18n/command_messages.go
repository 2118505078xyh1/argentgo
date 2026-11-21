package i18n

import (
	"fmt"
	"sort"
)

// 命令消息键。这些常量定义了 CommandMessage 可识别的完整键名集合，
// 与 catalog 的 UI key 分开维护：命令消息描述自研 shell 的执行结果，
// 由调用方按需取用，二者各自演进、互不干扰。
const (
	CommandShellTimeout      = "shell.timeout"
	CommandShellExitCode     = "shell.exit_code"
	CommandShellKilledSignal = "shell.killed_signal"
	CommandShellTruncated    = "shell.truncated"
	CommandShellNoOutput     = "shell.no_output"
	CommandShellSpillPath    = "shell.spill_path"
	CommandShellFailed       = "shell.failed"
	CommandShellStarted      = "shell.started"
	CommandShellCancelled    = "shell.cancelled"
)

// commandMessageEntry 是单个命令消息键在两种语言环境下的文案。
type commandMessageEntry struct {
	en string
	zh string
}

// commandMessageTable 把命令消息键映射到本地化文案。
//
// 每种语言的格式动词刻意保持一致，使同一组 args 在两种语言下都能正确
// 渲染。该表独立于 catalog，不参与现有文案审计，也不改变任何既有文案
// 字节。
var commandMessageTable = map[string]commandMessageEntry{
	CommandShellTimeout:      {"command timed out after %s", "命令执行超时：%s"},
	CommandShellExitCode:     {"exit code %d", "退出码 %d"},
	CommandShellKilledSignal: {"killed by signal %s", "被信号 %s 终止"},
	CommandShellTruncated:    {"output truncated to %d bytes", "输出已截断至 %d 字节"},
	CommandShellNoOutput:     {"command produced no output", "命令未产生输出"},
	CommandShellSpillPath:    {"full output written to %s", "完整输出已写入 %s"},
	CommandShellFailed:       {"command failed", "命令执行失败"},
	CommandShellStarted:      {"running command: %s", "正在执行命令：%s"},
	CommandShellCancelled:    {"command canceled", "命令已取消"},
}

// missingCommandMessage 为未知键构造稳定的占位串。占位串包含原始键，
// 使尚未翻译的消息在 UI 中可见而非渲染为空白；同一键总是得到同一串。
// 使用字节拼接而非格式化，避免对键内容做任何解释，也不会 panic。
func missingCommandMessage(key string) string {
	return "<missing command message: " + key + ">"
}

// CommandMessage 返回命令消息键在给定语言环境下的本地化文案。
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
func CommandMessage(lang, key string, args ...any) string {
	entry, ok := commandMessageTable[key]
	if !ok {
		return missingCommandMessage(key)
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

// CommandMessageKeys 返回所有命令消息键，按字典序稳定排序。
//
// 每次调用都从表中重新构造并排序，返回的是独立切片：调用方修改结果不会
// 影响表或后续调用。供审计遍历全部已定义键使用。
func CommandMessageKeys() []string {
	keys := make([]string, 0, len(commandMessageTable))
	for key := range commandMessageTable {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
