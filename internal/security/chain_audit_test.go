package security

import (
	"sync"
	"testing"
)

func TestAuditTraceInvariantOK(t *testing.T) {
	tests := []struct {
		name  string
		build func() *AuditTrace
		want  bool
	}{
		{
			name:  "zero value has no steps to violate",
			build: func() *AuditTrace { return &AuditTrace{} },
			want:  true,
		},
		{
			name:  "fresh trace is valid",
			build: NewAuditTrace,
			want:  true,
		},
		{
			name: "allow is audited and redacted",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("allow")
				tr.SetAudited()
				tr.SetRedacted()
				return tr
			},
			want: true,
		},
		{
			name: "policy deny stops before approval but audits",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("deny")
				tr.SetAudited()
				tr.SetRedacted()
				return tr
			},
			want: true,
		},
		{
			name: "policy deny without an audit entry",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("deny")
				return tr
			},
			want: false,
		},
		{
			name: "policy deny may not fall through to approval",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("deny")
				tr.SetApproval(true, true)
				tr.SetAudited()
				tr.SetRedacted()
				return tr
			},
			want: false,
		},
		{
			name: "approval granted then audited",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("ask")
				tr.SetApproval(true, true)
				tr.SetAudited()
				tr.SetRedacted()
				return tr
			},
			want: true,
		},
		{
			name: "approval refused then audited",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("ask")
				tr.SetApproval(true, false)
				tr.SetAudited()
				tr.SetRedacted()
				return tr
			},
			want: true,
		},
		{
			name: "approval asked but answer never reached the audit",
			build: func() *AuditTrace {
				return &AuditTrace{PolicyDecision: "ask", ApprovalAsked: true, ApprovalGranted: false}
			},
			want: false,
		},
		{
			name: "audited without redaction",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("allow")
				tr.SetAudited()
				return tr
			},
			want: false,
		},
		{
			name: "deny detection ignores case and surrounding space",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("  DENY  ")
				tr.SetApproval(true, false)
				tr.SetAudited()
				tr.SetRedacted()
				return tr
			},
			want: false,
		},
		{
			name: "CJK decision text is never treated as deny",
			build: func() *AuditTrace {
				tr := NewAuditTrace()
				tr.SetPolicy("策略拒绝：不允许写入")
				tr.SetApproval(true, false)
				tr.SetAudited()
				tr.SetRedacted()
				return tr
			},
			want: true,
		},
		{
			name:  "nil receiver has nothing to violate",
			build: func() *AuditTrace { return nil },
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.build().InvariantOK(); got != tt.want {
				t.Fatalf("InvariantOK() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestAuditTraceSettersAreNilSafe(t *testing.T) {
	var tr *AuditTrace
	tr.SetPolicy("deny")
	tr.SetApproval(true, true)
	tr.SetAudited()
	tr.SetRedacted()
	if !tr.InvariantOK() {
		t.Fatal("setters on a nil trace must not panic or corrupt the nil state")
	}
}

func TestAuditTraceInvariantOKIsReadOnly(t *testing.T) {
	tr := NewAuditTrace()
	tr.SetPolicy("ask")
	tr.SetApproval(true, false)
	tr.SetAudited()
	tr.SetRedacted()
	beforePolicy, beforeAsked, beforeGranted, beforeAudited, beforeRedacted :=
		tr.PolicyDecision, tr.ApprovalAsked, tr.ApprovalGranted, tr.Audited, tr.Redacted
	for i := 0; i < 3; i++ {
		if !tr.InvariantOK() {
			t.Fatal("a completed ask/refuse/audit/redact chain must satisfy the invariants")
		}
	}
	if tr.PolicyDecision != beforePolicy || tr.ApprovalAsked != beforeAsked ||
		tr.ApprovalGranted != beforeGranted || tr.Audited != beforeAudited ||
		tr.Redacted != beforeRedacted {
		t.Fatal("InvariantOK must not mutate the trace")
	}
}

func TestAuditTraceConcurrentUse(t *testing.T) {
	tr := NewAuditTrace()
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				switch worker % 5 {
				case 0:
					tr.SetPolicy("deny")
				case 1:
					tr.SetApproval(true, i%2 == 0)
				case 2:
					tr.SetAudited()
				case 3:
					tr.SetRedacted()
				case 4:
					// 并发交错时可能短暂处于违规状态，这里只验证无数据竞争与无 panic。
					_ = tr.InvariantOK()
				}
			}
		}(worker)
	}
	wg.Wait()
}
