package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// formatInspectionReason 把一次检查判定的发现 ID 和原因渲染为一条人类可读的上下文字符串。
// 它是纯函数：无 I/O、无修改、不依赖审批器。InspectionManager.Resolve 返回的原因和发现 ID
// 已经过脱敏和 rune 限长，因此本函数只是组合它们——绝不会重新引入原始参数文本。
//
// 该字符串仅用作操作人员的展示上下文（"为什么要问我？"），
// 绝不用作缓存键、权限范围或审计记录的输入。这些输入为何保持不变，见 askInspectionApproval。
//
//	原因 + ID -> "[security SEC-1234: destructive shell command: rm -rf /]"
//	仅原因   -> "[security: destructive shell command: rm -rf /]"
//	仅 ID    -> "[security SEC-1234]"
//	都没有   -> ""（没有需要解释的内容）
func formatInspectionReason(v security.Verdict) string {
	reason := oneLine(strings.TrimSpace(v.Reason))
	id := oneLine(strings.TrimSpace(v.FindingID))
	switch {
	case reason == "" && id == "":
		return ""
	case id == "":
		return "[security: " + reason + "]"
	case reason == "":
		return "[security " + id + "]"
	default:
		return "[security " + id + ": " + reason + "]"
	}
}

// oneLine 把判定字符串折叠为一行可打印文本，使多行原因不会破坏审批提示。
// 它保留每个可打印的 rune（包括 CJK），只把水平空白替换为空格并丢弃 ASCII 控制字节。
// 因为循环是按 rune 遍历的，所以 UTF-8 序列从不会被切断。
func oneLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// 丢弃其他 ASCII 控制字节；它们绝不能进入提示。
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// reasonAwareApprover 是 Approver 可选实现的能力，用于接收检查发现的理由，
// 作为与工具名和参数并列的展示上下文。实现方必须只根据 name 和 args 派生缓存键、权限范围和审计记录，
// 绝不能依据 reason：理由可能在两次其他方面完全相同的调用之间变化，
// 把它折进任何键都会使审批行为和审计偏离实际被授权的那次调用。
//
// 本仓库目前没有任何审批器实现这个接口，因此加入它对当前运行是刻意的空操作。
// 它是未来 security 侧带 reason 的 Ask 变体的安全接线点（那个包由另一个改动负责）；
// 在此之前，理由会被格式化并交给这个接缝，仍使用普通的 Ask。
type reasonAwareApprover interface {
	AskWithReason(ctx context.Context, name, args, reason string) (bool, error)
}

// askInspectionApproval 针对 InspectRequireApproval 判定，向操作人员询问一次，
// 并在审批器支持时附上该判定的发现上下文。关键是它原样传递 name 和 args。
// 防护器与 security.Chain 共享审批器，而 CachedApprover / ttlApprover / ScopedApprover
// 都以 name + 规范化后的 args 作为其授权（以及 ScopedApprover 的 deny/always 级别）的键。
// 给其中任一者追加 "[security: ...]" 前缀，都会为同一个工具调用创建不同的缓存键和权限范围，
// 因此刻意不这样做：理由只通过可选的 AskWithReason 接缝传递，从而不进入任何键。
func askInspectionApproval(ctx context.Context, approver security.Approver, tool string, args json.RawMessage, v security.Verdict) (bool, error) {
	if approver == nil {
		// 模仿 Chain.check 的空对象行为，而不是 panic。
		return security.UnavailableApprover{}.Ask(ctx, tool, string(args))
	}
	if aware, ok := approver.(reasonAwareApprover); ok {
		return aware.AskWithReason(ctx, tool, string(args), formatInspectionReason(v))
	}
	return approver.Ask(ctx, tool, string(args))
}
