package security

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

// 本文件是在现有 security Chain 之前额外增加的一层独立检查。它不实现策略、
// 审批或审计：Chain.Check 仍然负责这三者（策略 -> 审批 -> 审计），并且仍然是
// 工具注册表调用的单一关卡。管理器只多产生一个裁决，由组装根决定如何把它
// 并入该决策。
//
// 接线由 internal/app 完成（它是唯一允许组合内部包的包）：
//
//	verdict := inspectors.Resolve(ctx, toolName, args)
//	switch verdict.Action {
//	case security.InspectDeny:
//		// 检查器拒绝是硬性停止；运行不得重试。
//		return &security.StopError{Tool: toolName, Reason: verdict.Reason}
//	case security.InspectRequireApproval:
//		ok, err := approver.Ask(ctx, toolName, string(args))
//		if err != nil {
//			return err
//		}
//		if !ok {
//			return &security.StopError{Tool: toolName, Reason: verdict.Reason, Refused: true}
//		}
//		// 已批准：继续走链，执行策略 + 审计。
//	case security.InspectAllow:
//		// 没有额外关卡：继续走链。
//	}
//	return chain.Check(ctx, toolName, args)
//
// 已经回答过的 RequireApproval 可以通过一个返回所记录答案的审批器包装器重放给
// Chain.Check，这样操作员不会被提示两次。Deny 永远不会到达 Chain.Check。
// Allow 意味着"没有额外关卡"，绝不是"跳过链"：策略和审计仍然运行。
// 管理器从不写入链、审批器或审计日志。

// InspectionAction 是粗略的决策。值按严格程度排序，因此合并时较大的值总是
// 优先于较小的值。
type InspectionAction uint8

const (
	InspectAllow InspectionAction = iota
	InspectRequireApproval
	InspectDeny
)

// Verdict 是一个检查器的独立发现。所有字符串字段都会在裁决返回之前被
// InspectionManager.Resolve 限制长度并脱敏，因为原因和发现 ID 常常会出现在
// 日志和提示中。
type Verdict struct {
	Action     InspectionAction
	Reason     string
	Confidence float64
	Inspector  string
	FindingID  string
}

// Inspector 是一项独立检查。实现必须可并发安全使用，把 ctx 取消当作尽快
// 返回的提示，并让 Name 保持廉价且无副作用。错误或 panic 会被管理器记录并
// 跳过（失败即软失败）；它绝不会阻止其他检查器。
type Inspector interface {
	Name() string
	Inspect(ctx context.Context, tool string, args json.RawMessage) (Verdict, error)
}

// 这些限制防止某个检查器用巨大的请求或无界的原因字符串独占 Resolve。
// maxInspectionArgs 以原始字节计；字符串限制以 rune 计，因此 UTF-8 序列永远
// 不会被截断一半。
const (
	maxInspectionArgs      = 1 << 20 // 1 MiB
	maxVerdictReasonRunes  = 512
	maxVerdictNameRunes    = 128
	maxVerdictFindingRunes = 128
	maxFindingKindRunes    = 16
)

// InspectionManager 按注册顺序运行检查器并合并它们的裁决。它可并发安全使用：
// Add/Disable/SetErrorHandler 可以在 Resolve 运行时调用。Resolve 会先对配置
// 做快照，然后在不持锁的情况下运行检查器。
type InspectionManager struct {
	mu           sync.RWMutex
	inspectors   []Inspector
	disabled     map[string]bool
	onErr        func(string, error)
	newFindingID func(string) string // 可注入，便于确定性测试
}

// Add 把一个检查器注册到运行顺序的末尾。nil 检查器和名字为空的检查器会被
// 忽略。名字不必唯一：Disable 会跳过所有带该名字的检查器。重新添加不会重新
// 启用任何东西 —— 在本包增加显式的 Enable 方法之前，Disable 是粘性的。
func (m *InspectionManager) Add(inspector Inspector) {
	if m == nil || inspector == nil {
		return
	}
	name := safeInspectorName(inspector)
	if strings.TrimSpace(name) == "" {
		return
	}
	m.mu.Lock()
	m.inspectors = append(m.inspectors, inspector)
	m.mu.Unlock()
}

// Disable 跳过所有以 name 注册（包括现在和以后添加的）的检查器。
func (m *InspectionManager) Disable(name string) {
	if m == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	m.mu.Lock()
	if m.disabled == nil {
		m.disabled = make(map[string]bool)
	}
	m.disabled[name] = true
	m.mu.Unlock()
}

// SetErrorHandler 设置被跳过检查器（错误和 panic）的回调。nil 处理器会关闭
// 日志。回调在把错误写入任何持久位置之前应先脱敏；原始错误会被透传，
// 因此调用方仍然可以使用 errors.Is。
func (m *InspectionManager) SetErrorHandler(fn func(string, error)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onErr = fn
	m.mu.Unlock()
}

// Resolve 按顺序运行启用的检查器，并按 DENY 优先规则合并它们的裁决：
//
//  1. 任何 Deny 都胜出；多个 Deny 中置信度最高者胜出。
//  2. 否则任何 RequireApproval 胜出；其中置信度最高者胜出。
//  3. 否则置信度最高的 Allow 胜出。
//  4. 没有裁决（没有检查器、全部被禁用、全部出错/panic，或 context 已取消）
//     会失败即拒绝为一个 RequireApproval，FindingID 为空，原因为
//     "no inspector produced a verdict" —— goose 的未知请求规则。
//
// Allow 永远不会覆盖更严格的裁决。置信度相同时，注册顺序中第一个检查器胜出，
// 这使合并保持确定性。检查器出错或 panic 会通过 onErr（若设置）报告并跳过；
// 其余检查器仍会运行。超过 maxInspectionArgs 字节的请求绝不会交给检查器：
// 它们失败即拒绝为 RequireApproval。返回裁决的字符串字段会被限制长度并脱敏。
func (m *InspectionManager) Resolve(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if m == nil {
		return noVerdictVerdict()
	}
	if len(args) > maxInspectionArgs {
		return sanitizeVerdict(Verdict{
			Action:    InspectRequireApproval,
			Reason:    fmt.Sprintf("inspection arguments exceed the %d-byte limit", maxInspectionArgs),
			Inspector: "inspection-manager",
			FindingID: m.generateFindingID("oversize"),
		})
	}

	m.mu.RLock()
	snapshot := make([]Inspector, len(m.inspectors))
	copy(snapshot, m.inspectors)
	disabled := make(map[string]bool, len(m.disabled))
	for name, off := range m.disabled {
		disabled[name] = off
	}
	onErr := m.onErr
	newID := m.newFindingID
	m.mu.RUnlock()
	if newID == nil {
		newID = NewFindingID
	}

	verdicts := make([]Verdict, 0, len(snapshot))
	for _, inspector := range snapshot {
		if inspector == nil {
			continue
		}
		name := strings.TrimSpace(safeInspectorName(inspector))
		if disabled[name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			break
		}
		verdict, ok := inspectOne(ctx, inspector, name, tool, args, onErr)
		if ok {
			verdicts = append(verdicts, verdict)
		}
	}
	if len(verdicts) == 0 {
		return sanitizeVerdict(noVerdictVerdict())
	}

	merged := mergeVerdicts(verdicts)
	if merged.FindingID == "" {
		merged.FindingID = newID(actionKind(merged.Action))
	}
	return sanitizeVerdict(merged)
}

// mergeVerdicts 是纯函数：它从不回调检查器或管理器。未知的 Action 值会被
// 当作 RequireApproval，这样有缺陷的检查器无法把决策悄悄降级为 Allow。
func mergeVerdicts(verdicts []Verdict) Verdict {
	var allow, approval, deny *Verdict
	for i := range verdicts {
		v := verdicts[i]
		switch v.Action {
		case InspectAllow, InspectRequireApproval, InspectDeny:
		default:
			v.Action = InspectRequireApproval
		}
		if math.IsNaN(v.Confidence) || math.IsInf(v.Confidence, 0) {
			v.Confidence = 0
		}
		switch v.Action {
		case InspectDeny:
			if deny == nil || v.Confidence > deny.Confidence {
				deny = &v
			}
		case InspectRequireApproval:
			if approval == nil || v.Confidence > approval.Confidence {
				approval = &v
			}
		case InspectAllow:
			if allow == nil || v.Confidence > allow.Confidence {
				allow = &v
			}
		}
	}
	switch {
	case deny != nil:
		return *deny
	case approval != nil:
		return *approval
	default:
		return *allow
	}
}

// inspectOne 隔离单个检查器，使 panic 不会拖垮进程或跳过其余运行。
// 错误和 panic 都是失败即软失败。
func inspectOne(ctx context.Context, inspector Inspector, name, tool string, args json.RawMessage, onErr func(string, error)) (v Verdict, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			reportInspectorError(onErr, name, fmt.Errorf("security: inspector %q panicked: %v", name, r))
			v, ok = Verdict{}, false
		}
	}()
	v, err := inspector.Inspect(ctx, tool, args)
	if err != nil {
		reportInspectorError(onErr, name, err)
		return Verdict{}, false
	}
	return v, true
}

// reportInspectorError 防止行为异常的错误处理器把失败即软失败变成崩溃。
func reportInspectorError(onErr func(string, error), name string, err error) {
	if onErr == nil {
		return
	}
	defer func() { _ = recover() }()
	onErr(name, err)
}

func safeInspectorName(inspector Inspector) (name string) {
	defer func() {
		if recover() != nil {
			name = "unknown"
		}
	}()
	return inspector.Name()
}

func noVerdictVerdict() Verdict {
	return Verdict{Action: InspectRequireApproval, Reason: "no inspector produced a verdict"}
}

func actionKind(action InspectionAction) string {
	switch action {
	case InspectDeny:
		return "DENY"
	case InspectRequireApproval:
		return "APPROVAL"
	default:
		return "ALLOW"
	}
}

func sanitizeVerdict(v Verdict) Verdict {
	v.Reason = boundString(Redact(v.Reason), maxVerdictReasonRunes)
	v.Inspector = boundString(Redact(v.Inspector), maxVerdictNameRunes)
	v.FindingID = boundString(Redact(v.FindingID), maxVerdictFindingRunes)
	return v
}

// boundString 在 rune 边界截断，并为省略号留出空间，因此裁决绝不会在
// UTF-8 序列中间结束。
func boundString(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func (m *InspectionManager) generateFindingID(kind string) string {
	if m != nil {
		m.mu.RLock()
		gen := m.newFindingID
		m.mu.RUnlock()
		if gen != nil {
			return gen(kind)
		}
	}
	return NewFindingID(kind)
}

var findingCounter atomic.Uint64

// NewFindingID 返回一个有界的 SEC-<16 hex>[-<KIND>] 标识符。它使用
// crypto/rand，若系统随机源失败则回退到原子计数器。
func NewFindingID(kind string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		binary.BigEndian.PutUint64(b[:], findingCounter.Add(1))
	}
	id := "SEC-" + strings.ToUpper(hex.EncodeToString(b[:]))
	if k := findingKind(kind); k != "" {
		id += "-" + k
	}
	return id
}

func findingKind(kind string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(kind) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			if b.Len() >= maxFindingKindRunes {
				break
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
