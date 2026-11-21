package jobs

import "strings"

const (
	// cronMaxExprBytes 限制表达式字节数，避免对超长输入做无界扫描与分配。
	cronMaxExprBytes = 4096
	// cronFieldCountWithoutSeconds / cronFieldCountWithSeconds 是接受的字段数：
	// 五字段补前导秒，六字段保持字段值不变。
	cronFieldCountWithoutSeconds = 5
	cronFieldCountWithSeconds    = 6
)

// cronFieldByteAllowed 报告 b 是否是 cron 字段允许出现的字节。
// 本项目的 cron 语法只接受数字与 * , - /，因此任何非 ASCII 字节（含 CJK）都会被拒绝。
func cronFieldByteAllowed(b byte) bool {
	if b >= '0' && b <= '9' {
		return true
	}
	return b == '*' || b == ',' || b == '-' || b == '/'
}

// normalizeCronExpr 把 cron 表达式规范化为统一的六字段形式（含前导秒）。
//
// 规则：
//   - 五字段（分 时 日 月 周）在最前补 "0 " 作为秒；
//   - 六字段（秒 分 时 日 月 周）保持字段值不变；
//   - 字段数不是 5 或 6、含有语法外的字符（含 CJK 等非 ASCII 字节）、
//     或输入过长时返回空串。
//
// 字段按 Unicode 空白切分并用单个 ASCII 空格重新拼接，因此全角空格等 CJK
// 空白也能正确分隔，且输出与原始空白无关、对同一输入恒定（稳定）。函数不
// 修改入参、不持有全局状态；超长输入会被提前拒绝，扫描与分配均有界。
func normalizeCronExpr(expr string) string {
	if len(expr) == 0 || len(expr) > cronMaxExprBytes {
		return ""
	}
	fields := strings.Fields(expr)
	n := len(fields)
	if n != cronFieldCountWithoutSeconds && n != cronFieldCountWithSeconds {
		return ""
	}
	// 逐字段校验：每个字段非空，且只包含 cron 语法允许的 ASCII 字节。
	for _, field := range fields {
		for i := 0; i < len(field); i++ {
			if !cronFieldByteAllowed(field[i]) {
				return ""
			}
		}
	}
	body := strings.Join(fields, " ")
	if n == cronFieldCountWithoutSeconds {
		return "0 " + body
	}
	return body
}
