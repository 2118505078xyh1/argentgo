package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// PromptApprover 从注入的 reader 读取 y/n 回答，并把问题写到注入的
type Approver interface {
	Ask(ctx context.Context, name string, args string) (bool, error)
}

// writer，因此可测试，也可从 TUI 中使用。
// readMu 串行化对 In 的实际读取。被取消的 Ask 会让它的 reader
type PromptApprover struct {
	In  io.Reader
	Out io.Writer

	mu sync.Mutex
	// goroutine 卡在 readLine 中；没有这把锁，后续的 Ask 会派生第二个并发
	// reader，而泄漏的那个可能吞掉下一行回答。泄漏的 goroutine 会一直持有
	// readMu，直到它的读取结束。
	// NewPromptApprover 把提示连接到 reader 和 writer。
	readMu sync.Mutex
}

// lineResult 把一次 readLine 的结果从其 goroutine 带回。
func NewPromptApprover(in io.Reader, out io.Writer) *PromptApprover {
	return &PromptApprover{In: in, Out: out}
}

// Ask 打印问题并读取一行。只有 y/yes 表示批准；EOF 和空
type lineResult struct {
	line string
	err  error
}

// 回答表示拒绝。阻塞的 stdin 读取在 goroutine 中进行，因此被取消的
// context 会立即返回，而不是等待操作员输入。
// readLine 可能在交互式 stdin 读取上永远阻塞。把它放在 goroutine 中
func (p *PromptApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if p == nil || p.In == nil {
		return false, fmt.Errorf("security: no input configured for the approval prompt")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Out != nil {
		question, hadTags := SanitizeInvisible(fmt.Sprintf("Allow %s %s? [y/N] ", name, readableArgs(args)))
		if hadTags {
			question = "⚠ 请求包含不可见字符（已清除 Unicode 标签）\n" + question
		}
		if _, err := io.WriteString(p.Out, question); err != nil {
			return false, fmt.Errorf("security: writing approval prompt: %w", err)
		}
	}

	// 让 ctx.Done() 有机会赢得竞争。如果取消获胜，该 goroutine 会一直卡在
	// 那次读取上，直到 stdin 传来一行或关闭。在这个 goroutine 内部持有
	// readMu 使泄漏变得安全：后续 Ask 的 reader 会等待 readMu，而不是并发
	// 读取并偷走下一个答案。结果 channel 是带缓冲的，因此 goroutine 总能
	// 在没有接收者的情况下结束。
	// 与取消同时完成的读取仍必须报告取消，
	// 绝不能报告与之竞争的普通批准/拒绝。
	results := make(chan lineResult, 1)
	go func() {
		p.readMu.Lock()
		line, err := readLine(p.In)
		p.readMu.Unlock()
		results <- lineResult{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case result := <-results:
		// NeverApprover 拒绝所有请求。它支撑 ARGENT_APPROVAL=never。
		// Ask 总是拒绝，且不触碰 stdin。
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if result.err != nil && result.err != io.EOF {
			return false, fmt.Errorf("security: reading approval answer: %w", result.err)
		}
		answer := strings.ToLower(strings.TrimSpace(result.line))
		return answer == "y" || answer == "yes", nil
	}
}

// ErrApprovalUnavailable 是 UnavailableApprover 返回的哨兵错误。它
type NeverApprover struct{}

// 表示根本联系不到操作员（无头运行、子代理或审批通道损坏），这与操作员
func (NeverApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// 回答"否"有本质区别。ClassifyApproval 把它映射为 ApprovalUnavailable，
// 绝不会映射为 ApprovalRejected，使调用方能把两种情况分开。
// UnavailableApprover 模拟审批通道缺失或损坏。与
// NeverApprover（操作员说了"否"）不同，它报告无人能够回答。
// 因此链条把它审计为"unavailable"而非"refused"，并且不追加反对重试的
var ErrApprovalUnavailable = errors.New("security: approval channel unavailable")

// 终结性语句：通道可能恢复，因此重试与在真正拒绝后再次询问并非同一种错误。
// Ask 总是报告通道不可用，且不触碰 stdin。
// approvalKey 标识一次授权：工具名加上被批准的确切参数的规范化
// 形式。只用名称作为键会让一次无害的 "git status" 批准在同一会话中
// 悄悄授权后来的 "rm -rf /"。
type UnavailableApprover struct{}

// CachedApprover 在本会话剩余时间内记住已批准的 (tool, args) 对。
func (UnavailableApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, ErrApprovalUnavailable
}

// 拒绝永不被缓存，因此操作员可以改变主意；新会话必须使用新的
// CachedApprover。同一工具使用不同参数需要重新批准。
// NewCachedApprover 包装 inner。没有内部审批器时每次调用都被
// 拒绝。
type approvalKey struct {
	name     string
	argsHash string
}

// Ask 对本会话中已经批准过的完全相同的 (name, args) 对立即返回 true。
// 仅仅看到相同的工具名但参数不同，并不足以跳过提示。
// Allowed 列出本会话中至少有一组参数被批准的工具名。
// 名称会去重，因为每个名称可能有多组已批准的参数。
type CachedApprover struct {
	inner   Approver
	mu      sync.Mutex
	allowed map[approvalKey]bool
}

// canonicalArgs 规范化原始 JSON 参数对象，使同一调用的两种写法比较
// 相等：对象键递归排序，空白被去掉。非 JSON 参数的空白会被压缩。它
func NewCachedApprover(inner Approver) *CachedApprover {
	if inner == nil {
		inner = NeverApprover{}
	}
	return &CachedApprover{inner: inner, allowed: make(map[approvalKey]bool)}
}

// 有意放在 security 包本地，使本包永远不必依赖
// internal/agent（它拥有一个等价的辅助函数）。
// UseNumber 保持整数字面量精确；解码为 float64 可能让
func (c *CachedApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	key := approvalCacheKey(name, args)
	c.mu.Lock()
	allowed := c.allowed[key]
	c.mu.Unlock()
	if allowed {
		return true, nil
	}
	ok, err := c.inner.Ask(ctx, name, args)
	if err != nil || !ok {
		return false, err
	}
	c.mu.Lock()
	c.allowed[key] = true
	c.mu.Unlock()
	return true, nil
}

// 两个不同的数值参数规范化为同一字符串，从而共享一次批准。
// 拒绝尾随数据，与 json.Unmarshal 一致，使带后缀的 JSON 值
// 不会坍缩到裸值的缓存键上。
func (c *CachedApprover) Allowed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := make(map[string]bool, len(c.allowed))
	names := make([]string, 0, len(c.allowed))
	for key := range c.allowed {
		if seen[key.name] {
			continue
		}
		seen[key.name] = true
		names = append(names, key.name)
	}
	return names
}

// approvalCacheKey 把工具名与其规范化参数一起哈希，
// 使缓存中不保留任何原始（可能很大的）参数文本。
// ScopedApprover 是一个可移植的构件，它在任意 Approver 之上叠加内存中的
// 审批决策。它模仿 goose 的三级权限：
// never (Deny)、always (GrantAlways) 和 ask（委托给 inner），并有一个有界的
func canonicalArgs(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	// 中间地带 (GrantN)。Deny 是绝对的：被拒绝的工具名在每次 Ask 时都会被拒绝，
	// 在任何授予或内部审批器被查询之前，即使它之前曾被永久授予。授予和拒绝只
	// 存在于此包装器的生命周期内。它是有意附加的，并未接入默认安全链，
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	// 因此调用方必须显式选择加入，现有审批行为保持不变。
	// NewScopedApprover 包装 inner。没有内部审批器时委托调用会被
	if err := dec.Decode(new(any)); err != io.EOF {
		return strings.Join(strings.Fields(raw), " ")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return string(normalized)
}

// 拒绝，与 CachedApprover 的空对象行为一致。
// GrantAlways 在此包装器的生命周期内永久允许 name。
func approvalCacheKey(name, args string) approvalKey {
	normalized := canonicalArgs(args)
	sum := sha256.Sum256([]byte(name + "\x00" + normalized))
	return approvalKey{name: name, argsHash: hex.EncodeToString(sum[:])}
}

// GrantN 在接下来 n 次调用中允许 name。授予会累加：再次调用会把 n
// 加到剩余预算上。非正数 n 是空操作。
// Deny 记录对 name 的显式拒绝（goose 的 "never" 级别）。被拒绝的
// 名称在每次 Ask 时都会在任何授予或内部审批器被查询之前被拒绝。
// Deny 是粘性的且绝对的：之后的 GrantAlways 或 GrantN 不会重新启用它。
// 使用 RevokePrefix 清除拒绝。
// RevokePrefix 移除所有工具名以 prefix 开头的授予和拒绝。
// MCP 工具命名为 "server__tool"，因此卸载服务器时会调用
// RevokePrefix("server__") 来丢弃该服务器的所有批准和拒绝。
type ScopedApprover struct {
	inner   Approver
	mu      sync.Mutex
	always  map[string]bool
	limited map[string]int
	deny    map[string]bool
}

// 空前缀清空整个包装器。它返回被移除的 map 条目数
// （一个名称在它出现过的每个桶中都会被计一次）。
func NewScopedApprover(inner Approver) *ScopedApprover {
	if inner == nil {
		inner = NeverApprover{}
	}
	return &ScopedApprover{
		inner:   inner,
		always:  make(map[string]bool),
		limited: make(map[string]int),
		deny:    make(map[string]bool),
	}
}

// Ask 按 deny 优先的顺序应用权限级别：被拒绝的名称立即被
func (s *ScopedApprover) GrantAlways(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.always[name] = true
}

// 拒绝，然后是永久授予，再是 N 次授予（会被消费），否则调用委托给
// inner（"ask"）。永久授予胜过 N 次授予，且不改变 N 的预算。
func (s *ScopedApprover) GrantN(name string, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limited[name] += n
}

// readLine 读取一行而不提前缓冲，因此共享的 reader 会保留
// 属于下一次提示的字节。
// readableArgs 为审批提示渲染工具参数。它保留
// 换行符使多行命令保持可读，把制表符展开为空格，去掉
func (s *ScopedApprover) Deny(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deny[name] = true
}

// ANSI 转义序列和其他控制字节，并在 rune 边界处截断，
// 使宽的 UTF-8 字符永远不会被切成两半。
// 过长的参数绝不会只保留头部：调用方可以在 400 个无害字符后隐藏
// 一条破坏性命令，过去这会让审批提示只显示无害的前缀。可见预算在
// 头部和尾部之间分配，使操作员总能看到命令如何结束。结果
func (s *ScopedApprover) RevokePrefix(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for name := range s.always {
		if strings.HasPrefix(name, prefix) {
			delete(s.always, name)
			removed++
		}
	}
	for name := range s.limited {
		if strings.HasPrefix(name, prefix) {
			delete(s.limited, name)
			removed++
		}
	}
	for name := range s.deny {
		if strings.HasPrefix(name, prefix) {
			delete(s.deny, name)
			removed++
		}
	}
	return removed
}

// 保持在历史上的 runeCap+1 rune 上限之内。
// 把同样的 runeCap 预算用于可见内容（头 + 尾）；
// 标记和一个尾部省略号使整体大小保持为旧的 runeCap+1。
// runeHead 返回 s 的前 n 个 rune，若 s 更短则返回整个 s。它
func (s *ScopedApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	s.mu.Lock()
	if s.deny[name] {
		s.mu.Unlock()
		return false, nil
	}
	if s.always[name] {
		s.mu.Unlock()
		return true, nil
	}
	if remaining := s.limited[name]; remaining > 0 {
		s.limited[name] = remaining - 1
		s.mu.Unlock()
		return true, nil
	}
	s.mu.Unlock()
	return s.inner.Ask(ctx, name, args)
}

// 绝不会切开 UTF-8 序列。
// runeTail 返回 s 的最后 n 个 rune，若 s 更短则返回整个 s。它
func readLine(reader io.Reader) (string, error) {
	var out strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				return out.String(), nil
			}
			if buf[0] != '\r' {
				out.WriteByte(buf[0])
			}
		}
		if err != nil {
			return out.String(), err
		}
	}
}

// 绝不会切开 UTF-8 序列。
// stripANSIAndControls 移除 ANSI 转义序列（CSI、OSC 和裸 ESC
// 序列）以及控制字节，保留可打印文本、'\n' 和 '\t'。
// 当调用方传入非正数 ttl 时使用 defaultTTLGrantLifetime。
//
// maxTTLGrantEntries 限制内存中和磁盘上的授予集合大小。
// maxTTLCanonicalBytes 限制 Key 检查的参数文本量。
// maxTTLReadableRunes 限制每条授予保留的人类可读上下文。
// 位于头部与尾部之间，并带有一个明确的"省略 N 字符"标记
// ttlGrantsFile 是在 dir 内创建的持久化文件。
// 结果保持在历史上的 runeCap+1 rune 上限之内。
func readableArgs(args string) string {
	text := stripANSIAndControls(args)
	text = strings.ReplaceAll(text, "\t", "    ")
	const runeCap = 400
	const ellipsis = "…"
	total := utf8.RuneCountInString(text)
	if total <= runeCap {
		return text
	}

	marker := fmt.Sprintf("…省略%d字符…", total-runeCap)
	// 把同样的 runeCap 预算用于可见内容（头 + 尾）；
	// 标记和一个尾部省略号使整体大小保持为旧的 runeCap+1。
	budget := runeCap - utf8.RuneCountInString(marker)
	if budget < 0 {
		budget = 0
	}
	headRunes := budget / 2
	tailRunes := budget - headRunes
	return runeHead(text, headRunes) + marker + runeTail(text, tailRunes) + ellipsis
}

// runeHead 返回 s 的前 n 个 rune，若 s 更短则返回整个 s。它
// 绝不会切开 UTF-8 序列。
func runeHead(s string, n int) string {
	if n <= 0 {
		return ""
	}
	seen := 0
	for pos := range s {
		if seen == n {
			return s[:pos]
		}
		seen++
	}
	return s
}

// runeTail 返回 s 的最后 n 个 rune，若 s 更短则返回整个 s。它
// 绝不会切开 UTF-8 序列。
func runeTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	start := len(s)
	seen := 0
	for start > 0 && seen < n {
		_, size := utf8.DecodeLastRuneInString(s[:start])
		start -= size
		seen++
	}
	return s[start:]
}

// stripANSIAndControls 移除 ANSI 转义序列（CSI、OSC 和裸 ESC
// 序列）以及控制字节，保留可打印文本、'\n' 和 '\t'。
func stripANSIAndControls(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b { // ESC
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '[': // CSI：一直消费到 0x40..0x7e 范围内的结束字节
				i++
				for i < len(s) {
					last := s[i]
					i++
					if last >= 0x40 && last <= 0x7e {
						break
					}
				}
			case ']': // OSC：一直消费到 BEL (0x07) 或 ST (ESC '\\')
				i++
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default: // 其他转义序列：丢弃 ESC 和这一个字节
				i++
			}
			continue
		}
		if c == '\n' || c == '\t' {
			b.WriteByte(c)
			i++
			continue
		}
		if c < 0x20 || c == 0x7f {
			i++ // 其他任意 ASCII 控制字节
			continue
		}
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x80 && r <= 0x9f {
			i += size // 以 UTF-8 编码的 C1 控制字节
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}
