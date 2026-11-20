package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestStopErrorIncludesFinality 检查 StopError 保留其原始的
// 拒绝/否决措辞，同时携带防重试的终结语句。
func TestStopErrorIncludesFinality(t *testing.T) {
	tests := []struct {
		name     string
		err      *StopError
		wantVerb string
	}{
		{
			name:     "policy deny with reason",
			err:      &StopError{Tool: "bash", Reason: "tool is on the deny list"},
			wantVerb: "denied",
		},
		{
			name:     "policy deny without reason",
			err:      &StopError{Tool: "bash"},
			wantVerb: "denied",
		},
		{
			name:     "operator refusal",
			err:      &StopError{Tool: "write", Reason: "the operator refused the tool call", Refused: true},
			wantVerb: "refused",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.err.Error()
			if !strings.Contains(got, "the "+tc.err.Tool+" tool call was "+tc.wantVerb) {
				t.Fatalf("error %q lost the original %s wording", got, tc.wantVerb)
			}
			assertFinality(t, got)
		})
	}
}

// TestFinalitySentenceWording 锁定那些告诉模型拦截并非暂时性、
// 且不得重试的关键措辞。
func TestFinalitySentenceWording(t *testing.T) {
	if !strings.Contains(finalitySentence, "intentional") {
		t.Fatalf("finality sentence %q lacks \"intentional\"", finalitySentence)
	}
	if !strings.Contains(finalitySentence, "Do not retry") {
		t.Fatalf("finality sentence %q lacks \"Do not retry\"", finalitySentence)
	}
}

// TestChainDenialCarriesFinality 检查面向工具的接缝：Chain.Check 产生的
// 策略拒绝必须带着完整的终结文本到达模型。
func TestChainDenialCarriesFinality(t *testing.T) {
	dir := t.TempDir()
	chain := NewChain(
		NewPolicy(dir, PolicyConfig{DenyTools: []string{"bash"}}),
		&countingApprover{allow: true},
		NewAuditLog(dir),
		"s1",
	)
	err := chain.Check(context.Background(), "bash", json.RawMessage(`{"command":"ls"}`))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("got %v, want ErrDenied", err)
	}
	var stop *StopError
	if !errors.As(err, &stop) {
		t.Fatalf("got %#v, want a StopError", err)
	}
	assertFinality(t, err.Error())
}

func assertFinality(t *testing.T, got string) {
	t.Helper()
	if !strings.Contains(got, finalitySentence) {
		t.Fatalf("error %q does not carry the finality sentence", got)
	}
	if !strings.Contains(got, "intentional") || !strings.Contains(got, "Do not retry") {
		t.Fatalf("error %q lacks the anti-retry wording", got)
	}
}
