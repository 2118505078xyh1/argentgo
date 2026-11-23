package app

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// stagedApprover 是生产可用的一次性/始终/拒绝分阶段审批器。
// 它通过注入的 stdin/stdout 与操作员交互，把"始终允许"确认过的作用域
// 记在进程内集合中，使后续同作用域的调用免提示。它只在 opt-in 开启时
// 安装；默认路径不使用本类型，逐字节不变。
type stagedApprover struct {
	in   io.Reader
	out  io.Writer
	mu   sync.Mutex
	seen map[string]bool // 已记住的作用域（工具名或命令前缀）
}

var _ security.StagedApprover = (*stagedApprover)(nil)

func newStagedApprover(in io.Reader, out io.Writer) *stagedApprover {
	return &stagedApprover{in: in, out: out, seen: map[string]bool{}}
}

// Ask 先查已记住的作用域；命中即批准，否则委托给普通的 y/N 提示。
func (a *stagedApprover) Ask(ctx context.Context, name, args string) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	a.mu.Lock()
	remembered := a.seen[name]
	a.mu.Unlock()
	if remembered {
		return true, nil
	}
	if a.out != nil {
		fmt.Fprintf(a.out, "Allow %s %s? [y/N] ", name, strings.TrimSpace(args))
	}
	return readYesLine(ctx, a.in)
}

// AskStage 按阶段向操作员提问并解析其选择。
func (a *stagedApprover) AskStage(ctx context.Context, name, args string, stage security.ApprovalStage, summary string) (security.ApprovalDecision, error) {
	if ctx.Err() != nil {
		return security.ApprovalDecision{}, ctx.Err()
	}
	if a.out != nil {
		switch stage {
		case security.StageOnce:
			fmt.Fprintf(a.out, "Approve %s? [once/always/reject] (esc=back): ", name)
		case security.StageConfirmScope:
			fmt.Fprintf(a.out, "Remember scope: %s — confirm? [y/N] (esc=back): ", summary)
		case security.StageFeedback:
			fmt.Fprint(a.out, "Reason for rejection (empty to stay): ")
		}
	}
	line, err := readStageLine(ctx, a.in)
	if err != nil {
		return security.ApprovalDecision{}, err
	}
	choice := strings.ToLower(strings.TrimSpace(line))
	switch stage {
	case security.StageOnce:
		switch choice {
		case "always":
			return security.ApprovalDecision{Choice: security.ChoiceAlways, Scope: []string{name}}, nil
		case "reject":
			return security.ApprovalDecision{Choice: security.ChoiceReject}, nil
		case "esc":
			return security.ApprovalDecision{Choice: security.ChoiceEsc}, nil
		default: // once / y / yes / 空
			return security.ApprovalDecision{Choice: security.ChoiceOnce}, nil
		}
	case security.StageConfirmScope:
		if choice == "esc" {
			return security.ApprovalDecision{Choice: security.ChoiceEsc}, nil
		}
		if choice == "y" || choice == "yes" {
			return security.ApprovalDecision{Choice: security.ChoiceAlways, Confirmed: true}, nil
		}
		return security.ApprovalDecision{Choice: security.ChoiceAlways}, nil
	case security.StageFeedback:
		if choice == "esc" {
			return security.ApprovalDecision{Choice: security.ChoiceEsc}, nil
		}
		return security.ApprovalDecision{Choice: security.ChoiceReject, Feedback: line, Confirmed: strings.TrimSpace(line) != ""}, nil
	}
	return security.ApprovalDecision{Choice: security.ChoiceOnce}, nil
}

// RememberScope 把二次确认通过的作用域记入内存集合。
func (a *stagedApprover) RememberScope(scopes []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range scopes {
		if s = strings.TrimSpace(s); s != "" {
			a.seen[s] = true
		}
	}
}

func readYesLine(ctx context.Context, in io.Reader) (bool, error) {
	line, err := readStageLine(ctx, in)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func readStageLine(ctx context.Context, in io.Reader) (string, error) {
	if in == nil {
		return "", io.EOF
	}
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		scanner := bufio.NewScanner(in)
		if scanner.Scan() {
			ch <- result{line: scanner.Text()}
		} else if err := scanner.Err(); err != nil {
			ch <- result{err: err}
		} else {
			ch <- result{err: io.EOF}
		}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err == io.EOF && r.line != "" {
			return r.line, nil
		}
		return r.line, r.err
	}
}
