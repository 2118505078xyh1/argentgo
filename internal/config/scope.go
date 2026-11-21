package config

import "strings"

// scopePriorityTable 把规范化（去首尾空白、转小写）后的作用域名称映射到
// 其优先级：default 最低（0），flag 最高（4）。数值越大，覆盖能力越强。
//
// 表中同时收录常见别名：global 等价于 user，local 等价于 project，
// cli 等价于 flag。所有键都是 ASCII；含 CJK 字符的输入经规范化后
// 不会命中表中任何条目，因此会作为未知作用域返回 -1。
var scopePriorityTable = map[string]int{
	"default": 0,
	"user":    1, "global": 1,
	"project": 2, "local": 2,
	"env":  3,
	"flag": 4, "cli": 4,
}

// maxScopePriorityKeyLen 限制参与查表的作用域名称长度（按字节）。
// 超过该长度的输入一律视为未知，避免超长输入造成无界的哈希开销。
// 该判断只比较字节长度、不切分字符串，因此不会破坏 CJK 字符的
// UTF-8 编码（CJK 安全）。
const maxScopePriorityKeyLen = 64

// scopePriority 返回配置作用域 scope 的优先级，数值越大优先级越高：
//
//	default(0) < user(1) < project(2) < env(3) < flag(4)
//
// 匹配大小写不敏感，并忽略首尾空白（含 Unicode 空白，如全角空格
// U+3000）。别名 global/local/cli 分别映射到 user/project/flag。
// 未知作用域（包括空串、近似拼写、超长输入以及含 CJK 字符的名称）
// 返回 -1。
//
// 该函数是纯函数：不读取配置、环境或磁盘，不修改任何状态，
// 相同输入始终得到相同输出。所有字符串操作都按 rune/UTF-8 安全
// 的方式进行，含 CJK 的输入既不会 panic，也不会被错误截断后
// 误判成某个已知作用域。
func scopePriority(scope string) int {
	name := strings.TrimSpace(scope)
	if len(name) == 0 || len(name) > maxScopePriorityKeyLen {
		return -1
	}
	priority, ok := scopePriorityTable[strings.ToLower(name)]
	if !ok {
		return -1
	}
	return priority
}
