package textutil

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// dataURLsElisionEnv 是「裁剪前整段替换 data URL」的独立门控变量。
const dataURLsElisionEnv = "ARGENT_ELIDE_DATA_URLS"

// DataURLsElisionEnabled 报告 data URL 原子替换是否开启。
func DataURLsElisionEnabled() bool {
	return strings.TrimSpace(os.Getenv(dataURLsElisionEnv)) == "1"
}

// dataURLRE 匹配完整的 base64 data URL。payload 至少 64 个字符才替换，
// 避免把很短的合法引用误伤。
var dataURLRE = regexp.MustCompile(`data:([A-Za-z0-9.!#$&^_+\-]+/[A-Za-z0-9.!#$&^_+\-]+);base64,([A-Za-z0-9+/=]{64,})`)

// dataURLFragmentRE 匹配历史上被裁碎的 data URL 前缀（含空 payload），
// 用于治愈遗留碎片，避免它们继续在后续请求中被提供方拒绝。
var dataURLFragmentRE = regexp.MustCompile(`data:([A-Za-z0-9.!#$&^_+\-]+/[A-Za-z0-9.!#$&^_+\-]+);base64,`)

// placeholder 刻意不含 "data:" 字面量，避免第二轮替换命中自己的输出。
func dataURLPlaceholder(mime string, payloadChars int) string {
	if mime == "" {
		mime = "unknown"
	}
	return fmt.Sprintf("[data URL elided: %s, %d base64 chars]", mime, payloadChars)
}

// ElideDataURLs 把文本中每个完整的 base64 data URL 原子替换为确定性占位，
// 保留 MIME 与 base64 字符数，使后续 head/tail 裁剪切点不会落在 base64 中间，
// 产生「看起来合法但永远无法解码」的坏图引用。随后再把遗留的 data URL 前缀
// 碎片一并替换为占位。无 data URL 时返回原字符串（不分配）。
func ElideDataURLs(s string) string {
	if !strings.Contains(s, "data:") {
		return s
	}
	out := dataURLRE.ReplaceAllStringFunc(s, func(match string) string {
		m := dataURLRE.FindStringSubmatch(match)
		if m == nil {
			return match
		}
		return dataURLPlaceholder(m[1], len(m[2]))
	})
	return dataURLFragmentRE.ReplaceAllStringFunc(out, func(match string) string {
		m := dataURLFragmentRE.FindStringSubmatch(match)
		if m == nil {
			return match
		}
		return dataURLPlaceholder(m[1], 0)
	})
}
