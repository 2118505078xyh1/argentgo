package agent

import "strings"

// reflectionUnfixableMarkers 列出"改变下一次调用也无法消除"的错误片段。
// 认证失败、网络不可达与配置错误都属于根本性阻断：模型改代码或改参数都无济于事，
// 把这类错误包装成"修复后重试"的反思反馈只会白白消耗一轮反思预算。
// 标记保持为小写英文片段或直接的中文词，配合 strings.ToLower 使用。
var reflectionUnfixableMarkers = []string{
	// 认证与授权：凭据/权限问题不能靠重试或修改本轮调用解决。
	"unauthorized", "authentication failed", "auth failed", "authentication error",
	"invalid credentials", "invalid api key", "invalid token", "token expired",
	"expired token", "permission denied", "access denied", "forbidden",
	"login failed", "401", "403",
	"认证失败", "鉴权失败", "身份验证失败", "未授权", "密钥无效", "令牌过期",
	"权限不足", "拒绝访问",

	// 网络：对端不可达或传输失败，本地改动无法让网络恢复。
	"network unreachable", "host unreachable", "unreachable", "connection refused",
	"connection reset", "connection timed out", "connection failed", "no such host",
	"name resolution", "dial tcp", "dns", "network error", "network is down",
	"offline", "timeout", "timed out",
	"网络不可达", "无法连接", "连接失败", "连接被拒绝", "连接超时", "网络错误",
	"网络异常", "主机不可达", "域名解析失败", "超时",

	// 配置：环境或配置文件本身的问题，不属于本轮生成的代码产物。
	"configuration error", "config error", "invalid config", "invalid configuration",
	"missing config", "configuration missing", "misconfigured", "environment variable",
	"配置错误", "配置无效", "配置缺失", "缺少配置", "配置不正确",
}

// reflectionFixableMarkers 列出"模型可通过修改代码/格式/工具参数来修复"的错误片段。
// 只有识别出这种可操作信号，才值得注入反思反馈并消耗一次反思预算。
var reflectionFixableMarkers = []string{
	// 代码与编译错误：模型可以直接改源码。
	"syntax error", "syntaxerror", "compile error", "compilation error",
	"compilation failed", "build failed", "failed to compile", "type error",
	"typeerror", "undefined", "undeclared", "parse error", "parse failed",
	"unexpected token", "cannot find module", "file not found",
	"语法错误", "编译失败", "编译错误", "类型错误", "未定义", "代码错误",
	"解析失败", "解析错误",

	// 编辑错误：锚点/旧文本匹配失败，模型可改写编辑内容后重试。
	"edit failed", "edit error", "anchor not found", "no match found",
	"编辑失败", "编辑错误", "锚点", "未找到匹配", "匹配失败",

	// 格式错误：编辑/输出格式不合要求，模型可以按规范重写。
	"format error", "edit format", "malformed", "bad format", "invalid format",
	"formatting", "invalid json", "json parse", "yaml parse",
	"格式错误", "格式不正确", "格式无效", "格式有误", "格式",

	// 工具参数错误：调用参数不合法，模型可以修正参数后重试。
	"bad arguments", "invalid arguments", "invalid argument", "missing argument",
	"missing required", "missing field", "invalid parameter", "unknown parameter",
	"unexpected argument", "argument error",
	"参数无效", "参数错误", "参数缺失", "缺少参数", "工具参数", "参数",
}

// reflectionWorthy 判断一条错误信息是否值得消耗一次反思预算。
//
// 反思回路的价值在于让模型依据反馈修正下一次尝试，因此只有当错误源于模型
// 能改动的内容（代码、格式或工具参数）时，注入"修复后重试"反馈才可能奏效。
// 认证失败、网络不可达和配置错误无法靠改变调用来消除，注入反思只会浪费预算
// 并把不可操作的反馈塞回上下文，故返回 false。
//
// 分类基于文本：错误信息以纯字符串形式跨过工具边界，没有可用的类型化信号。
// 匹配前先把整串折叠为小写并查找子串，因此英文大小写不敏感；中文标记直接按
// 字节包含，全程不按下标切分，故对 CJK 多字节序列安全。两张标记表都是固定
// 顺序的切片，线性扫描不依赖映射遍历顺序，结果可复现。
//
// 判定规则：
//   - 空串或仅含空白 -> false（没有可操作的信息）；
//   - 命中任一不可修复标记 -> false（优先：根本性阻断无法靠重试消除）；
//   - 命中任一可修复标记 -> true；
//   - 其余未知错误 -> false（保守：没有可修复证据就不占用反思预算）。
//
// 该函数是纯函数：无 I/O、无共享可变状态，相同输入总是产生相同结果。
func reflectionWorthy(errMsg string) bool {
	msg := strings.ToLower(strings.TrimSpace(errMsg))
	if msg == "" {
		return false
	}
	if containsAny(msg, reflectionUnfixableMarkers...) {
		return false
	}
	return containsAny(msg, reflectionFixableMarkers...)
}
