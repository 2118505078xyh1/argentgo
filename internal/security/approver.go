package security

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
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
}

// NewPromptApprover wires the prompt to a reader and writer.
func NewPromptApprover(in io.Reader, out io.Writer) *PromptApprover {
	return &PromptApprover{In: in, Out: out}
}

// Ask prints the question and reads one line. Only y/yes approves; EOF and an
// empty answer deny.
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
		question := fmt.Sprintf("Allow %s %s? [y/N] ", name, compactArgs(args))
		if _, err := io.WriteString(p.Out, question); err != nil {
			return false, fmt.Errorf("security: writing approval prompt: %w", err)
		}
	}
	line, err := readLine(p.In)
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("security: reading approval answer: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
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

// compactArgs keeps the prompt on one line.
func compactArgs(args string) string {
	text := strings.Join(strings.Fields(args), " ")
	const limit = 160
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
