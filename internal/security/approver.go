package security

import (
	"context"
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
		question := fmt.Sprintf("Allow %s %s? [y/N] ", name, readableArgs(args))
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

// CachedApprover remembers approved tool names for the rest of the session.
// A refusal is never cached, so the operator can change their mind; a new
// session must use a new CachedApprover.
type CachedApprover struct {
	inner   Approver
	mu      sync.Mutex
	allowed map[string]bool
}

// NewCachedApprover wraps inner. Without an inner approver every call is
// denied.
func NewCachedApprover(inner Approver) *CachedApprover {
	if inner == nil {
		inner = NeverApprover{}
	}
	return &CachedApprover{inner: inner, allowed: make(map[string]bool)}
}

// Ask returns true immediately for a tool name already approved this session.
func (c *CachedApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	c.mu.Lock()
	allowed := c.allowed[name]
	c.mu.Unlock()
	if allowed {
		return true, nil
	}
	ok, err := c.inner.Ask(ctx, name, args)
	if err != nil || !ok {
		return false, err
	}
	c.mu.Lock()
	c.allowed[name] = true
	c.mu.Unlock()
	return true, nil
}

// Allowed lists the tool names already approved this session.
func (c *CachedApprover) Allowed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.allowed))
	for name := range c.allowed {
		names = append(names, name)
	}
	return names
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
func readableArgs(args string) string {
	text := stripANSIAndControls(args)
	text = strings.ReplaceAll(text, "\t", "    ")
	const runeCap = 400
	const ellipsis = "…"
	if utf8.RuneCountInString(text) <= runeCap {
		return text
	}
	cut := 0
	seen := 0
	for i := range text {
		if seen == runeCap {
			cut = i
			break
		}
		seen++
	}
	return text[:cut] + ellipsis
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
