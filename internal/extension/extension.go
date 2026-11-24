// Package extension 提供四套扩展体系（skills/hooks/commands/plugins）的统一观察门面。
//
// 它不取代任何一套扩展的内部实现，也不取代 internal/app 的顶层装配地位；
// 它把四套里同构的部分收敛为统一类型，让调用方用同一组类型观察"扩展清单"与
// "信任门"，而不必知道每套各自的 Registry/Set/Wiring 形状。
//
// 设计依据见 docs/PLAN_EXTENSION_FACADE.md。实现期做了一个实证校正：
// skills/commands/plugins 是"目录扫描 → 命名条目"，可用 Descriptor 统一；
// hooks 是"事件→命令触发器"，并非命名条目，它的独特性在信任门——因此 hooks
// 只接入 Authorizer，不伪装成 Discoverer。门面因此小而诚实：输出统一 + 信任统一。
// 本包绝不 import app/agent/tools/llm。
package extension

// Kind 标识扩展条目的来源体系。
type Kind uint8

const (
	KindSkill Kind = iota + 1
	KindHook
	KindCommand
	KindPlugin
)

func (k Kind) String() string {
	switch k {
	case KindSkill:
		return "skill"
	case KindHook:
		return "hook"
	case KindCommand:
		return "command"
	case KindPlugin:
		return "plugin"
	default:
		return "unknown"
	}
}

// TrustState 是条目信任状态的可选信息。四套里只有 hooks 有真正的授权门，
// 其余体系的条目默认为 Unknown——这不是"不信任"，而是"无信任概念"。
type TrustState uint8

const (
	TrustUnknown TrustState = iota
	TrustVerified
	TrustUntrusted
)

// Descriptor 是命名条目的统一最小描述（skills/commands/plugins 共有 name/path）。
// 正文、工具白名单、事件触发等丰富元数据留在各自体系内，不塞进门面。
type Descriptor struct {
	Kind  Kind
	Name  string
	Path  string
	Trust TrustState
}

// DiagnosticKind 区分非致命问题的性质。
type DiagnosticKind uint8

const (
	DiagnosticSkipped   DiagnosticKind = iota // 条目被跳过（先到先得/损坏）
	DiagnosticInvalid                         // 格式/清单校验失败
	DiagnosticUntrusted                       // 未过信任门
	DiagnosticConflict                        // 重名/遮蔽冲突
)

func (d DiagnosticKind) String() string {
	switch d {
	case DiagnosticSkipped:
		return "skipped"
	case DiagnosticInvalid:
		return "invalid"
	case DiagnosticUntrusted:
		return "untrusted"
	case DiagnosticConflict:
		return "conflict"
	default:
		return "unknown"
	}
}

// Diagnostic 统一四套的"非致命问题"形状：plugins.Problem、hooks 的 warn、
// skills/commands 的静默跳过都可归约为它。它只描述问题，不改变任何行为。
type Diagnostic struct {
	Kind    DiagnosticKind
	Source  string
	Message string
}

// Authorizer 是信任门，只对应 hooks 的 (key,digest) 授权。
// Enabled 为 false 的实现（NoopAuthorizer）表示"该体系无信任概念"，
// 调用方据此决定是否执行授权，而非把它当作"已授信"。
type Authorizer interface {
	Authorize(key, digest string) error
	Enabled() bool
}

// Inventory 是统一扩展清单：命名条目 + 非致命诊断。它是四套发现的只读投影，
// 供诊断/日志/可观测使用，不替代各体系的运行态集合（toolset 仍用具体类型）。
type Inventory struct {
	Descriptors []Descriptor
	Diagnostics []Diagnostic
}
