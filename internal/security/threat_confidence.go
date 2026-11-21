package security

import "strings"

// 危险模式置信度的字符串取值。上层可以按这些值做风险决策，而不必依赖
// internal 包内部的 ThreatConfidence 整型枚举。
const (
	threatConfidenceHigh    = "high"
	threatConfidenceMedium  = "medium"
	threatConfidenceLow     = "low"
	threatConfidenceUnknown = "unknown"
)

// threatConfidenceRule 描述一类危险模式关键词及其置信度等级。keywords 中的
// 关键词按子串匹配，命中任意一个即判定为该规则对应的等级。
type threatConfidenceRule struct {
	level    string
	keywords []string
}

// threatConfidenceRules 按置信度从高到低排列。靠前的规则先被检查，因此当一段
// 模式同时包含多个等级的关键词时，始终返回其中最高的等级。顺序固定、不依赖
// map 遍历，所以同一输入永远得到同一结果。
var threatConfidenceRules = buildThreatConfidenceRules([]threatConfidenceRule{
	{
		level: threatConfidenceHigh,
		keywords: []string{
			"/dev/tcp",                                        // 反弹 shell 的设备重定向
			"reverse shell", "reverse-shell", "反弹shell", "反弹", // 反弹 shell
			"download-exec", "下载执行", // 下载即执行
			"curl|bash", "wget|sh", "curl | bash", "wget | sh",
			"|bash", "| bash", "|sh", "| sh", "| sudo sh", "| sudo bash", // 管道传给 shell
			"encoded-exec", "编码执行", // 解码后执行
			"base64 -d", "base64 --decode", "base64 -di", "xxd -r",
			"nc -e", "nc --exec", "ncat -e", "ncat --exec", "socat", "-exec", // 反弹 shell 工具
			"rm -rf /", "rm -f /", "rm -rf ~", "rm -f ~", "rm --force /", "rm -rf /*", // 大范围强制删除
		},
	},
	{
		level: threatConfidenceMedium,
		keywords: []string{
			"forced-rm", "强制删除", "rm -f", "rm -rf", "--force", // 强制删除（具体目标）
			"wrapper", "包装器", "sudo", "nohup ", "env ", // 经包装器提权
			"download", "下载", "curl", "wget", // 单纯下载，尚不构成下载即执行
		},
	},
	{
		level: threatConfidenceLow,
		keywords: []string{
			"rm", "rmdir", "mv", "cp", "dd", "chmod", "chown", "chgrp",
			"truncate", "shred", "unlink", "ln", "touch", // POSIX 修改类命令
			"del", "erase", "move", "copy", "xcopy", "ren", "rename", // cmd.exe 等价命令
			"删除", "修改", "写入", // 中文语义关键词
		},
	},
})

// buildThreatConfidenceRules 在包初始化时把所有关键词统一转为小写，这样运行时
// 只需对输入做一次 strings.ToLower 即可完成大小写不敏感匹配。返回的切片与规则表
// 同序，保证优先级在初始化后依然固定。
func buildThreatConfidenceRules(rules []threatConfidenceRule) []threatConfidenceRule {
	out := make([]threatConfidenceRule, len(rules))
	for i, rule := range rules {
		out[i] = threatConfidenceRule{
			level:    rule.level,
			keywords: make([]string, len(rule.keywords)),
		}
		for j, keyword := range rule.keywords {
			out[i].keywords[j] = strings.ToLower(keyword)
		}
	}
	return out
}

// threatConfidence 根据危险模式字符串中的关键词返回其置信度等级：
// "high"、"medium"、"low" 或 "unknown"。
//
// 语义与实现要点：
//   - 大小写不敏感：输入和关键词都经过 strings.ToLower（Unicode 简单大小写折叠）。
//   - CJK 安全：strings.ToLower 与 strings.Contains 都按 UTF-8 处理，中文关键词
//     （如"反弹""下载执行""删除"）可直接匹配；全过程不做字节切片，因此不会
//     切断中文字符或破坏 UTF-8 编码。
//   - 稳定：规则按固定顺序检查，命中即返回；无全局状态、无 map 遍历，重复调用
//     同一输入必定得到同一结果。
//   - 空串、纯空白或不含任何已知关键词的模式返回 "unknown"。
//
// 该函数是纯函数：不读写文件、环境变量或其他外部状态，也不修改任何数据。
func threatConfidence(pattern string) string {
	lower := strings.ToLower(strings.TrimSpace(pattern))
	if lower == "" {
		return threatConfidenceUnknown
	}
	for _, rule := range threatConfidenceRules {
		for _, keyword := range rule.keywords {
			if keyword != "" && strings.Contains(lower, keyword) {
				return rule.level
			}
		}
	}
	return threatConfidenceUnknown
}
