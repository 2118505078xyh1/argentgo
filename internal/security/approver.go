package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// Approver asks a human whether one tool call may run.
type Approver interface {
	Ask(ctx context.Context, name string, args string) (bool, error)
}

// PromptApprover reads a y/n answer from an injected reader and writes the
// question to an injected writer, so it is testable and usable from a TUI.
type PromptApprover struct {
	In  io.Reader
	Out io.Writer

	mu sync.Mutex
	// readMu serialises the actual reads from In. A cancelled Ask leaves its
	// reader goroutine parked in readLine; without this lock a later Ask would
	// spawn a second concurrent reader and the leaked one could swallow the
	// next answer line. The leaked goroutine keeps readMu until its read ends.
	readMu sync.Mutex
}

// NewPromptApprover wires the prompt to a reader and writer.
func NewPromptApprover(in io.Reader, out io.Writer) *PromptApprover {
	return &PromptApprover{In: in, Out: out}
}

// lineResult carries one readLine outcome back from its goroutine.
type lineResult struct {
	line string
	err  error
}

// Ask prints the question and reads one line. Only y/yes approves; EOF and an
// empty answer deny. The blocking stdin read runs in a goroutine so a cancelled
// context returns promptly instead of waiting for operator input.
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

	// readLine can block forever on an interactive stdin read. Running it in a
	// goroutine lets ctx.Done() win the race. If cancellation wins, the
	// goroutine stays blocked on that single read until stdin delivers a line
	// or closes. Holding readMu inside that goroutine makes the leak safe: a
	// later Ask's reader waits for readMu instead of reading concurrently and
	// stealing the next answer. The result channel is buffered so the goroutine
	// can always finish without a receiver.
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
		// A read that finished at the same moment as cancellation must still
		// report the cancellation, never a plain approve/deny racing with it.
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

// NeverApprover denies every request. It backs ARGENT_APPROVAL=never.
type NeverApprover struct{}

// Ask always refuses without touching stdin.
func (NeverApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return false, nil
}

// approvalKey identifies one authorization: a tool name plus the canonical
// form of the exact arguments that were approved. Keying by name alone would
// let a harmless "git status" approval silently authorize "rm -rf /" later in
// the same session.
type approvalKey struct {
	name     string
	argsHash string
}

// CachedApprover remembers approved (tool, args) pairs for the rest of the
// session. A refusal is never cached, so the operator can change their mind;
// a new session must use a new CachedApprover. Different arguments to the same
// tool require a fresh approval.
type CachedApprover struct {
	inner   Approver
	mu      sync.Mutex
	allowed map[approvalKey]bool
}

// NewCachedApprover wraps inner. Without an inner approver every call is
// denied.
func NewCachedApprover(inner Approver) *CachedApprover {
	if inner == nil {
		inner = NeverApprover{}
	}
	return &CachedApprover{inner: inner, allowed: make(map[approvalKey]bool)}
}

// Ask returns true immediately for an exact (name, args) pair already approved
// this session. Merely seeing the same tool name with different arguments is
// not enough to skip the prompt.
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

// Allowed lists the tool names with at least one approved argument set this
// session. Names are deduplicated because each name may have several approved
// argument sets.
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

// canonicalArgs canonicalizes a raw JSON argument object so two spellings of
// the same call compare equal: object keys are sorted recursively and
// whitespace is dropped. Non-JSON arguments are whitespace-collapsed. It is
// intentionally local to security so this package never has to depend on
// internal/agent (which owns an equivalent helper).
func canonicalArgs(raw string) string {
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

// approvalCacheKey hashes the tool name together with its canonical arguments
// so no raw (possibly very large) argument text is retained in the cache.
func approvalCacheKey(name, args string) approvalKey {
	normalized := canonicalArgs(args)
	sum := sha256.Sum256([]byte(name + "\x00" + normalized))
	return approvalKey{name: name, argsHash: hex.EncodeToString(sum[:])}
}

// ScopedApprover is a portable building block that layers in-memory approval
// grants on top of any Approver: a tool name can be permanently allowed
// (GrantAlways) or allowed for a bounded number of calls (GrantN). Grants live
// only for the lifetime of this wrapper. It is deliberately additive and is
// not wired into the default security chain, so callers opt in explicitly and
// existing approval behaviour is unchanged.
type ScopedApprover struct {
	inner   Approver
	mu      sync.Mutex
	always  map[string]bool
	limited map[string]int
}

// NewScopedApprover wraps inner. Without an inner approver delegated calls are
// denied, matching CachedApprover's null-object behaviour.
func NewScopedApprover(inner Approver) *ScopedApprover {
	if inner == nil {
		inner = NeverApprover{}
	}
	return &ScopedApprover{
		inner:   inner,
		always:  make(map[string]bool),
		limited: make(map[string]int),
	}
}

// GrantAlways permanently allows name for the lifetime of this wrapper.
func (s *ScopedApprover) GrantAlways(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.always[name] = true
}

// GrantN allows name for the next n calls. Grants accumulate: a second call
// adds n to whatever budget remains. Non-positive n is a no-op.
func (s *ScopedApprover) GrantN(name string, n int) {
	if n <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limited[name] += n
}

// Ask consumes a grant for name when one exists and approves without asking
// inner; otherwise it delegates to inner. An always grant wins over an N grant
// and leaves the N budget untouched.
func (s *ScopedApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	s.mu.Lock()
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

// readLine reads one line without buffering ahead, so a shared reader keeps
// the bytes that belong to the next prompt.
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

// readableArgs renders tool arguments for the approval prompt. It keeps
// newlines so multiline commands stay legible, expands tabs to spaces, drops
// ANSI escape sequences and other control bytes, and truncates on a rune
// boundary so a wide UTF-8 character is never cut in half.
//
// Overlong arguments are never reduced to their head alone: a caller can hide
// a destructive command after 400 innocuous characters, which used to make the
// approval prompt show only the harmless prefix. The visible budget is split
// between the head and the tail and an explicit "省略 N 字符" marker sits
// between them, so the operator always sees how the command ends. The result
// stays within the historical runeCap+1 rune bound.
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
	// Spend the same runeCap budget on visible content (head + tail); the
	// marker and one trailing ellipsis keep the old outer size of runeCap+1.
	budget := runeCap - utf8.RuneCountInString(marker)
	if budget < 0 {
		budget = 0
	}
	headRunes := budget / 2
	tailRunes := budget - headRunes
	return runeHead(text, headRunes) + marker + runeTail(text, tailRunes) + ellipsis
}

// runeHead returns the first n runes of s, or all of s if it is shorter. It
// never splits a UTF-8 sequence.
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

// runeTail returns the last n runes of s, or all of s if it is shorter. It
// never splits a UTF-8 sequence.
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

// stripANSIAndControls removes ANSI escape sequences (CSI, OSC and bare ESC
// sequences) and control bytes, keeping printable text, '\n' and '\t'.
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
			case '[': // CSI: consumed through a final byte in 0x40..0x7e.
				i++
				for i < len(s) {
					last := s[i]
					i++
					if last >= 0x40 && last <= 0x7e {
						break
					}
				}
			case ']': // OSC: consumed through BEL (0x07) or ST (ESC '\\').
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
			default: // Other escape sequences: drop ESC and this one byte.
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
			i++ // any other ASCII control byte
			continue
		}
		if c < 0x80 {
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x80 && r <= 0x9f {
			i += size // C1 control byte encoded in UTF-8
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String()
}
