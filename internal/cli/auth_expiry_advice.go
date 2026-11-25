package cli

import (
	"fmt"
	"strings"

	"example.com/argentgo/internal/auth"
)

// authExpiryAdvice 把凭证过期状态翻译成一句可操作的中文提示，
// 供 401 / needs_auth 场景的调用方选择是否追加到错误输出中。
//
// 它是纯函数：只读参数，不访问凭证存储、时钟、配置或全局状态，
// 因此既能安全地用于表驱动测试，也能在将来被 401 输出路径 opt-in
// 调用，而不改变当前的字节级输出（当前尚未接线，见接线点报告）。
//
// status 使用 auth.TokenState，也就是 auth.TokenStatus 函数返回的
// 分类类型。文案为简体中文，与 writeActionableError 现有的错误提示
// 保持一致；若将来接入 i18n，可在调用方做 locale 映射，而不改变
// 本函数的纯函数语义。
//
// 返回值约定：
//   - TokenExpired：必须重新登录；
//   - TokenExpiring：建议提前刷新；
//   - TokenUnknown：状态未知（例如 API Key 没有截止时间），提示先检查状态；
//   - TokenValid 及任何无法识别的取值：返回空串，表示无需额外提示。
//
// provider 可能是 CJK 名称或带空白的输入。这里只做 Unicode 安全的
// TrimSpace 和 %s 插值，绝不按字节截断，因此多字节字符不会被切断。
func authExpiryAdvice(provider string, status auth.TokenState) string {
	name := strings.TrimSpace(provider)
	switch status {
	case auth.TokenExpired:
		if name == "" {
			return "凭证已过期，请运行 argentgo login 重新登录后重试"
		}
		return fmt.Sprintf("提供方 %s 的凭证已过期，请运行 argentgo login --provider %s 重新登录", name, name)
	case auth.TokenExpiring:
		if name == "" {
			return "凭证即将过期，建议运行 argentgo login 提前刷新"
		}
		return fmt.Sprintf("提供方 %s 的凭证即将过期，建议运行 argentgo login --provider %s 提前刷新", name, name)
	case auth.TokenUnknown:
		if name == "" {
			return "凭证状态未知（如 API Key 无截止时间），请运行 argentgo auth 查看状态，必要时用 argentgo login 重新登录"
		}
		return fmt.Sprintf("提供方 %s 的凭证状态未知（如 API Key 无截止时间），请运行 argentgo auth 查看状态，必要时用 argentgo login --provider %s 重新登录", name, name)
	default:
		// TokenValid 以及未来新增或无法识别的状态都不需要额外提示。
		return ""
	}
}
