package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/i18n"
)

// cliHintsEnvVar 是 CLI 错误修复建议的输出门控开关。按用户至上的 UX 策略，
// 此纯改善功能默认开启，只有显式设为 "0"（允许首尾空白）才关闭。
const cliHintsEnvVar = "ARGENT_CLI_HINTS"

// cliHintI18nKeys 是错误建议消息键的完整集合，与 cli_error_hint.go 的
// 四类分支一一对应。
var cliHintI18nKeys = [...]string{
	i18n.ErrorHintTLS,
	i18n.ErrorHintFile,
	i18n.ErrorHintInteractive,
	i18n.ErrorHintCredentials,
}

// cliHintToI18nKey 把 cliErrorHint 返回的简体中文建议映射到 i18n 消息键。
//
// 映射键取 i18n 的 zh-CN 文案：i18n 表承诺其文案与 cli_error_hint.go 的
// 四条硬编码建议逐字节一致，所以这里既不需要复制中文字符串，也不依赖
// cliErrorHintRules 的排列顺序或优先级。映射在包初始化时构造一次。
var cliHintToI18nKey = buildCliHintToI18nKey()

func buildCliHintToI18nKey() map[string]string {
	m := make(map[string]string, len(cliHintI18nKeys))
	for _, key := range cliHintI18nKeys {
		m[i18n.ErrorHintMessage("zh-CN", key)] = key
	}
	return m
}

// cliHintsEnabled 报告是否开启了 CLI 错误建议输出。策略变更（用户至上）：
// 纯 UX 改善默认开启，只有把 ARGENT_CLI_HINTS 显式设为 "0"（允许首尾空白）
// 才关闭。env 为 nil 时按默认开启处理，调用方无需自行判空。
func cliHintsEnabled(env func(string) string) bool {
	if env == nil {
		return true
	}
	return strings.TrimSpace(env(cliHintsEnvVar)) != "0"
}

// writeActionableErrorEnv 在 writeActionableError 的既有两行输出之后，
// 按 locale 追加一条与错误分支对应的本地化修复建议。
//
// 门控关闭、错误为 nil、或错误未命中任何已知分支时，输出与
// writeActionableError 逐字节相同：先原样调用它写出 Message/NextStep，
// 再决定是否追加，因此既有的 "error: ..." 行和分类说明始终不变。
func writeActionableErrorEnv(w io.Writer, err error, env func(string) string, locale string) {
	writeActionableError(w, err)
	if err == nil || !cliHintsEnabled(env) {
		return
	}
	key, ok := cliHintToI18nKey[cliErrorHint(err.Error())]
	if !ok {
		return
	}
	fmt.Fprintln(w, i18n.ErrorHintMessage(locale, key))
}
