package agent

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// defaultRepeatWindow 是重复防护在其滑动窗口中保留的最近工具调用数量。
// 只有该窗口内的重复才计入阻断阈值，因此很久以前的一串重复调用
// 不会在同一运行中阻塞很久之后的合法重试。
const defaultRepeatWindow = 10

// dedupe 是重复防护器。两个独立信号共同决定同一结果：
//
//   - 参数窗口统计最近调用中的（工具, 规范化参数）对；某个签名在该窗口中重复超过
//     max 次，就会被判定为"卡在同一个调用"的循环并阻断；
//   - 输出窗口按输出哈希对某个工具最近的结果分组。当超过 max 组不同参数产生了相同输出时，
//     说明模型只是在制造伪进展（例如反复读取不同文件却都返回相同内容），下一次调用也会被阻断。
//
// 以错误结果回应的调用绝不会再次访问工作区，从而避免卡住的模型耗尽整个轮次预算。
type dedupe struct {
	max    int
	window int

	// 基于参数的环形缓冲区，保存 normalizedKey 值。
	ring   []string
	head   int
	filled int
	counts map[string]int

	// 基于输出的环形缓冲区，每个工具名一个。
	outputs map[string]*outputWindow
}

// outputWindow 保存某个工具最近的（规范化参数, 输出哈希）对。
// 用哈希代替输出本身，这样不会保留庞大的结果。
type outputWindow struct {
	entries []outputEntry
	head    int
	filled  int
}

type outputEntry struct {
	normalized string
	hash       string
}

func newDedupe(maxRepeats int) *dedupe {
	return newDedupeWindow(maxRepeats, defaultRepeatWindow)
}

// newDedupeWindow 是可指定窗口的构造函数。newDedupe 是包内其余代码使用的轻量包装；
// 提供这个形式是为了让测试能选用较小的窗口，无需冗长的脚本运行即可验证淘汰逻辑。
func newDedupeWindow(maxRepeats, window int) *dedupe {
	if maxRepeats <= 0 {
		maxRepeats = defaultMaxRepeats
	}
	if window <= 0 {
		window = defaultRepeatWindow
	}
	return &dedupe{
		max:     maxRepeats,
		window:  window,
		ring:    make([]string, window),
		counts:  make(map[string]int),
		outputs: make(map[string]*outputWindow),
	}
}

// NormalizeArgs 规范化原始 JSON 参数对象，使同一调用的两种写法可比较相等：
// 递归排序对象的键并去除空白。非 JSON 参数则折叠空白。
func NormalizeArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}

// normalizedKey 构建防护器中缓存键路径和原始参数路径共用的映射键。
// 把它集中在一处可保证两者逐字节一致。
func normalizedKey(name, normalized string) string {
	return name + "\x00" + normalized
}

// recordNormalized 把 (name, normalized) 追加到参数窗口，窗口满时淘汰最旧的条目，
// 并返回当前窗口中相同签名的数量。
func (d *dedupe) recordNormalized(name, normalized string) int {
	key := normalizedKey(name, normalized)
	if d.filled == d.window {
		old := d.ring[d.head]
		d.counts[old]--
		if d.counts[old] <= 0 {
			delete(d.counts, old)
		}
	} else {
		d.filled++
	}
	d.ring[d.head] = key
	d.head = (d.head + 1) % d.window
	d.counts[key]++
	return d.counts[key]
}

// outputWouldBlock 报告对 name 的最近调用是否在超过 max 组不同参数下返回了相同输出。
// 这种模式意味着模型在改变请求，但工具始终回答同样的内容，即仅靠参数签名无法发现的伪进展循环。
func (d *dedupe) outputWouldBlock(name string) bool {
	ow := d.outputs[name]
	if ow == nil || ow.filled == 0 {
		return false
	}
	byHash := make(map[string]map[string]bool)
	for i := 0; i < ow.filled; i++ {
		idx := (ow.head - 1 - i + len(ow.entries)) % len(ow.entries)
		entry := ow.entries[idx]
		if entry.hash == "" {
			continue
		}
		args := byHash[entry.hash]
		if args == nil {
			args = make(map[string]bool)
			byHash[entry.hash] = args
		}
		args[entry.normalized] = true
	}
	for _, args := range byHash {
		if len(args) > d.max {
			return true
		}
	}
	return false
}

// recordOutput 把已执行调用的结果附加到输出窗口。
// 对 nil/已禁用的防护器以及调用方无法提供输出的情况，它是空操作；它本身从不阻断任何东西。
func (d *dedupe) recordOutput(name, normalized, output string) {
	if d == nil || d.max <= 0 {
		return
	}
	ow := d.outputs[name]
	if ow == nil {
		ow = &outputWindow{entries: make([]outputEntry, d.window)}
		d.outputs[name] = ow
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(output))
	ow.entries[ow.head] = outputEntry{normalized: normalized, hash: strconv.FormatUint(h.Sum64(), 16)}
	ow.head = (ow.head + 1) % len(ow.entries)
	if ow.filled < len(ow.entries) {
		ow.filled++
	}
}

// blockedMessage 是所有重复阻断共用的唯一措辞。两个信号使用同一条消息，
// 保持了记录契约：无论哪个信号触发，调用方都按 "duplicate call blocked" 匹配。
func (d *dedupe) blockedMessage(name string) string {
	return fmt.Sprintf(
		"[duplicate call blocked] %s was already called %d times with the same arguments in this run; use the earlier result instead of calling it again",
		name, d.max)
}

// wouldBlockNormalized 适用于调用方已规范化参数的情形，相当于 wouldBlock。
// 它把参数窗口向前推演一次调用（考虑窗口满时的淘汰），但不记录任何内容。
func (d *dedupe) wouldBlockNormalized(name, normalized string) bool {
	if d == nil || d.max <= 0 {
		return false
	}
	key := normalizedKey(name, normalized)
	projected := d.counts[key] + 1
	if d.filled == d.window && d.ring[d.head] == key {
		// 加入本次调用会淘汰一个相同的最旧条目，再补回一个。
		projected--
	}
	return projected > d.max
}

// checkNormalized 适用于调用方已规范化参数的情形，相当于 check。
// 它把调用记录到参数窗口，并报告是否必须以错误结果回应而非执行。
func (d *dedupe) checkNormalized(name, normalized string) (blocked bool, message string) {
	if d == nil || d.max <= 0 {
		return false, ""
	}
	count := d.recordNormalized(name, normalized)
	if count > d.max || d.outputWouldBlock(name) {
		return true, d.blockedMessage(name)
	}
	return false, ""
}

// wouldBlock 报告下一次相同调用是否会被阻断，但不做记录。用于决定某批次能否并行运行。
func (d *dedupe) wouldBlock(name, args string) bool {
	if d == nil || d.max <= 0 {
		return false
	}
	return d.wouldBlockNormalized(name, NormalizeArgs(args))
}

// check 记录一次请求，并报告是否必须以错误结果回应而非执行。
func (d *dedupe) check(name, args string) (blocked bool, message string) {
	if d == nil || d.max <= 0 {
		return false, ""
	}
	return d.checkNormalized(name, NormalizeArgs(args))
}
