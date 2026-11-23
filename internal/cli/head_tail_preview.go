package cli

import (
	"fmt"
	"strings"
)

// headTailHiddenMarker 是头尾预览中省略段的标记模板；%d 会被替换为被隐藏的行数。
const headTailHiddenMarker = "... (%d lines hidden) ..."

// 头尾预览默认保留的行数；仅在显式开启预览时生效。
const (
	headTailDefaultHead = 8
	headTailDefaultTail = 8
)

// headTailPreviewEnv 是 sessions show 头尾预览的门控环境变量名。按用户至上的
// UX 策略，此纯改善功能默认开启，只有显式设为 "0"/"false" 才关闭。
const headTailPreviewEnv = "ARGENT_SESSIONS_HEAD_TAIL"

// headTailPreview 生成 lines 的头尾预览：保留最前面的 head 行与最后面的
// tail 行，省略的中间部分用 headTailHiddenMarker 标记替换。
//
// 行为约定：
//   - head、tail 为负数时按 0 处理，大于行数时按行数处理；
//   - 行数不足（总数 <= head+tail）时不省略，原样返回全部行；
//   - 按“行”操作，一行是完整字符串，因此不会在 UTF-8 字符（含 CJK）中间
//     截断，天然是 CJK 安全的；
//   - 返回文本用 "\n" 连接各行，不带结尾换行。
func headTailPreview(lines []string, head, tail int) string {
	total := len(lines)
	if head < 0 {
		head = 0
	}
	if tail < 0 {
		tail = 0
	}
	if head > total {
		head = total
	}
	if tail > total {
		tail = total
	}

	// 行数不足以同时保留头尾时，全部输出，不插入省略标记。
	if total <= head+tail {
		return strings.Join(lines, "\n")
	}

	hidden := total - head - tail
	parts := make([]string, 0, head+tail+1)
	parts = append(parts, lines[:head]...)
	parts = append(parts, fmt.Sprintf(headTailHiddenMarker, hidden))
	parts = append(parts, lines[total-tail:]...)
	return strings.Join(parts, "\n")
}

// sessionsHeadTailEnabled 读取头尾预览开关；按用户至上的 UX 策略默认开启，
// 只有显式设置为假值（0/false，忽略大小写与首尾空白）才关闭。
func sessionsHeadTailEnabled(app App) bool {
	switch strings.ToLower(strings.TrimSpace(app.env(headTailPreviewEnv))) {
	case "0", "false":
		return false
	default:
		return true
	}
}

// applyHeadTailToOutput 把 sessions show 的完整输出 content 转成头尾预览。
// content 的每一行以换行结束；这里去掉结尾换行后按行交给 headTailPreview，
// 再补回一个结尾换行，保持与旧输出一致的换行约定。
func applyHeadTailToOutput(content string) string {
	if content == "" {
		return ""
	}
	body := strings.TrimSuffix(content, "\n")
	lines := strings.Split(body, "\n")
	return headTailPreview(lines, headTailDefaultHead, headTailDefaultTail) + "\n"
}
