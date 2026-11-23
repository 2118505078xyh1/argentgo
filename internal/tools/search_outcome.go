package tools

import (
	"context"
	"errors"
	"strings"
)

// searchOutcome 承载搜索原语的截断信号。它刻意与输出字符串分离，
// 使内部调用方能拿到结构化的 truncated + limit，同时保持既有输出格式兼容。
type searchOutcome struct {
	Truncated bool   // 是否因达到上限而截断了结果
	Limit     int    // 生效的上限（结果条数）
	TimedOut  bool   // 是否因 context 超时提前结束
	RegexHint string // 正则降级的可读提示（lookaround / 字面量兜底等）
}

// probeLimit 返回"limit+1 探测"所需的采集上限：多收集一条仅用于判断
// 是否还有更多结果，返回给调用方前会裁回 limit。
func probeLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit == maxInt() {
		return limit
	}
	return limit + 1
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

// finalize 把探测到的命中裁回上限并填充截断信号。
func (o searchOutcome) finalize(count, limit int) searchOutcome {
	if limit <= 0 {
		return o
	}
	o.Limit = limit
	if count > limit {
		o.Truncated = true
	}
	return o
}

// searchTimeoutHint 在 context 超时时给出中文行动建议；其它情况返回空串。
func searchTimeoutHint(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "搜索超时：建议缩小 path 范围、增加更具体的 include 过滤或简化 pattern 后重试。"
	}
	return ""
}

// searchNoteSuffix 生成附加在输出最后一行的截断/超时提示。
// 刻意不新增行，以保持现有 200 行上限 golden 的输出格式不变。
func searchNoteSuffix(outcome searchOutcome, ctx context.Context) string {
	var parts []string
	if outcome.RegexHint != "" {
		parts = append(parts, outcome.RegexHint)
	}
	if outcome.Truncated && outcome.Limit > 0 {
		parts = append(parts, "truncated: reached "+itoa(outcome.Limit)+"-result limit; narrow pattern or path")
	}
	if hint := searchTimeoutHint(ctx); hint != "" {
		parts = append(parts, hint)
	}
	if len(parts) == 0 {
		return ""
	}
	return "  [" + strings.Join(parts, "; ") + "]"
}

// appendSearchNote 把提示附加到最后一条结果行的行尾。若原输出以换行
// 结尾则保留该换行，因此 '\n' 计数与行数都不变，现有 200 行 golden 兼容。
func appendSearchNote(out string, outcome searchOutcome, ctx context.Context) string {
	suffix := searchNoteSuffix(outcome, ctx)
	if suffix == "" || out == "" {
		return out
	}
	hadTrailingNL := strings.HasSuffix(out, "\n")
	body := strings.TrimSuffix(out, "\n")
	res := body + suffix
	if hadTrailingNL {
		res += "\n"
	}
	return res
}

// itoa 是 strconv.Itoa 的轻量别名，避免在每个搜索文件里重复 import strconv。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
