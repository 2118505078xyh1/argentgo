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

// This file is an ADDITIONAL, independent inspection layer in front of the
// existing security Chain. It does not implement policy, approval or audit:
// Chain.Check still owns all three (policy -> approval -> audit) and is still
// the single gate the tool registry calls. The manager only produces one more
// verdict that the composition root may fold into that decision.
//
// Wiring, done by internal/app (the only package allowed to compose internals):
//
//	verdict := inspectors.Resolve(ctx, toolName, args)
//	switch verdict.Action {
//	case security.InspectDeny:
//		// Inspector deny is a hard stop; the run must not retry.
//		return &security.StopError{Tool: toolName, Reason: verdict.Reason}
//	case security.InspectRequireApproval:
//		ok, err := approver.Ask(ctx, toolName, string(args))
//		if err != nil {
//			return err
//		}
//		if !ok {
//			return &security.StopError{Tool: toolName, Reason: verdict.Reason, Refused: true}
//		}
//		// Approved: fall through to the chain for policy + audit.
//	case security.InspectAllow:
//		// No extra gate: fall through to the chain.
//	}
//	return chain.Check(ctx, toolName, args)
//
// A RequireApproval that was already answered can be replayed to Chain.Check
// through an approver wrapper that returns the recorded answer, so the
// operator is not prompted twice. Deny never reaches Chain.Check. Allow means
// "no additional gate", never "skip the chain": policy and audit still run.
// The manager never writes to the chain, the approver or the audit log.

// InspectionAction is a coarse decision. Values are ordered by strictness so
// a larger value always outranks a smaller one when merging.
type InspectionAction uint8

const (
	InspectAllow InspectionAction = iota
	InspectRequireApproval
	InspectDeny
)

// Verdict is one inspector's independent finding. All string fields are
// bounded and redacted by InspectionManager.Resolve before the verdict is
// returned, because reasons and finding IDs commonly reach logs and prompts.
type Verdict struct {
	Action     InspectionAction
	Reason     string
	Confidence float64
	Inspector  string
	FindingID  string
}

// Inspector is one independent check. Implementations must be safe for
// concurrent use, treat ctx cancellation as a hint to return quickly, and
// keep Name cheap and free of side effects. An error or a panic is logged by
// the manager and skipped (fail-soft); it never stops the other inspectors.
type Inspector interface {
	Name() string
	Inspect(ctx context.Context, tool string, args json.RawMessage) (Verdict, error)
}

// Limits keep one inspector from monopolising Resolve with a huge request or
// an unbounded reason string. maxInspectionArgs is measured in raw bytes;
// string limits are measured in runes so UTF-8 sequences are never cut in half.
const (
	maxInspectionArgs      = 1 << 20 // 1 MiB
	maxVerdictReasonRunes  = 512
	maxVerdictNameRunes    = 128
	maxVerdictFindingRunes = 128
	maxFindingKindRunes    = 16
)

// InspectionManager runs inspectors in registration order and merges their
// verdicts. It is safe for concurrent use: Add/Disable/SetErrorHandler may be
// called while Resolve is running. Resolve snapshots the configuration and
// runs inspectors without holding the lock.
type InspectionManager struct {
	mu           sync.RWMutex
	inspectors   []Inspector
	disabled     map[string]bool
	onErr        func(string, error)
	newFindingID func(string) string // injectable for deterministic tests
}

// Add registers an inspector at the end of the run order. Nil inspectors and
// ones with an empty name are ignored. Names need not be unique: Disable
// skips every inspector carrying the given name. Re-adding re-enables nothing
// — Disable is sticky until this package grows an explicit Enable method.
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

// Disable skips every inspector registered under name (now and later added).
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

// SetErrorHandler installs the callback for skipped inspectors (errors and
// panics). A nil handler silences logging. The callback should redact before
// writing the error anywhere durable; the original error is passed through so
// callers can still use errors.Is.
func (m *InspectionManager) SetErrorHandler(fn func(string, error)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onErr = fn
	m.mu.Unlock()
}

// Resolve runs the enabled inspectors in order and merges their verdicts with
// DENY-DOMINANT rules:
//
//  1. Any Deny wins; among denies, the highest confidence wins.
//  2. Else any RequireApproval wins; among those, highest confidence wins.
//  3. Else the highest-confidence Allow wins.
//  4. No verdicts (no inspectors, all disabled, all errored/panicked, or an
//     already-cancelled context) is a fail-closed RequireApproval with an
//     empty FindingID and reason "no inspector produced a verdict" — goose's
//     unknown-request rule.
//
// Allow never overrides a stricter verdict. On equal confidence the first
// inspector in registration order wins, which keeps merges deterministic.
// An inspector error or panic is reported through onErr (if set) and skipped;
// the remaining inspectors still run. Requests larger than maxInspectionArgs
// bytes are never handed to inspectors: they fail closed to RequireApproval.
// The returned verdict's string fields are bounded and redacted.
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

// mergeVerdicts is pure: it never calls back into inspectors or the manager.
// Unknown action values are treated as RequireApproval so a buggy inspector
// cannot silently downgrade a decision to Allow.
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

// inspectOne isolates a single inspector so a panic cannot take down the
// process or skip the rest of the run. Both errors and panics are fail-soft.
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

// reportInspectorError keeps a misbehaving error handler from turning a
// fail-soft skip into a crash.
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

// boundString truncates on a rune boundary and leaves room for an ellipsis,
// so a verdict never ends in the middle of a UTF-8 sequence.
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

// NewFindingID returns a bounded SEC-<16 hex>[-<KIND>] identifier. It uses
// crypto/rand, falling back to an atomic counter if the system source fails.
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
