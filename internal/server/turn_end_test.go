package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestClassifyTurnEndThreeReasons(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		canceled bool
		want     TurnEndReason
	}{
		{"complete", nil, false, TurnEndComplete},
		{"failure", errors.New("provider exploded"), false, TurnEndFailure},
		{"interrupted by error", context.Canceled, false, TurnEndInterrupted},
		{"interrupted by flag", nil, true, TurnEndInterrupted},
		{"interrupted by both", context.Canceled, true, TurnEndInterrupted},
		{"flag dominates error", errors.New("boom"), true, TurnEndInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnEnd(tc.err, tc.canceled); got != tc.want {
				t.Fatalf("ClassifyTurnEnd(%v, %v) = %q, want %q", tc.err, tc.canceled, got, tc.want)
			}
		})
	}
}

func TestClassifyTurnEndWrappedCanceled(t *testing.T) {
	base := fmt.Errorf("agent loop: %w", context.Canceled)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"one wrap", base},
		{"two wraps", fmt.Errorf("transport: %w", base)},
		{"joined", errors.Join(errors.New("partial"), context.Canceled)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnEnd(tc.err, false); got != TurnEndInterrupted {
				t.Fatalf("ClassifyTurnEnd(%v, false) = %q, want interrupted", tc.err, got)
			}
		})
	}
}

func TestClassifyTurnEndNilError(t *testing.T) {
	if got := ClassifyTurnEnd(nil, false); got != TurnEndComplete {
		t.Fatalf("ClassifyTurnEnd(nil, false) = %q, want complete", got)
	}
}

func TestClassifyTurnEndDoesNotMisjudge(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"plain error named like cancellation", errors.New("context canceled")},
		{"plain error named like operation canceled", errors.New("operation was canceled")},
		{"deadline", context.DeadlineExceeded},
		{"wrapped deadline", fmt.Errorf("provider: %w", context.DeadlineExceeded)},
		{"deadline wording only", errors.New("context deadline exceeded")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTurnEnd(tc.err, false); got != TurnEndFailure {
				t.Fatalf("ClassifyTurnEnd(%v, false) = %q, want failure", tc.err, got)
			}
		})
	}
}

// outcomeRunner 立即返回一个固定的错误，使 ACP 提示无需门控或
// 真实的取消时序即可到达其终态结果。
type outcomeRunner struct{ err error }

func (r outcomeRunner) Prompt(context.Context, string, func(string)) (string, error) {
	return "partial", r.err
}
func (r outcomeRunner) Abort() {}

// TestACPTerminalResultCarriesTurnEndReason 验证附加字段出现在
// 两种结果形态的终态结果上，而原始的 stopReason 保持
// 不变。失败路径仍是 -32603 错误，code/message 完整；
// 只有附加的 error.data 携带 "failure" 分类。
func TestACPTerminalResultCarriesTurnEndReason(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantStop   string
		wantReason TurnEndReason
	}{
		{"complete", nil, "end_turn", TurnEndComplete},
		{"interrupted", fmt.Errorf("wrapped: %w", context.Canceled), "cancelled", TurnEndInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inR, inW := io.Pipe()
			outR, outW := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- RunACP(context.Background(), inR, outW, Config{Runner: outcomeRunner{err: tc.err}})
			}()
			defer inW.Close()

			enc := json.NewEncoder(inW)
			dec := json.NewDecoder(outR)
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
			readJSON(t, dec)

			_ = enc.Encode(map[string]any{
				"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
				"params": map[string]any{"prompt": "hi"},
			})
			var result map[string]any
			for {
				msg := readJSON(t, dec)
				if msg["id"] == float64(2) {
					result = msg
					break
				}
			}
			body, _ := result["result"].(map[string]any)
			if body["stopReason"] != tc.wantStop {
				t.Fatalf("stopReason = %v, want %q (result %v)", body["stopReason"], tc.wantStop, result)
			}
			if body["turnEndReason"] != string(tc.wantReason) {
				t.Fatalf("turnEndReason = %v, want %q (result %v)", body["turnEndReason"], tc.wantReason, result)
			}
			_ = inW.Close()
			waitFor(t, done)
		})
	}
}

// TestFailureTurnEndDataIsFailure 是下面线协议测试的纯辅助函数版本：
// 无需传输即可获得失败分类。
func TestFailureTurnEndDataIsFailure(t *testing.T) {
	data := failureTurnEndData(TurnEndFailure)
	if data.TurnEndReason != TurnEndFailure {
		t.Fatalf("failureTurnEndData reason = %q, want %q", data.TurnEndReason, TurnEndFailure)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal turn end data: %v", err)
	}
	if string(encoded) != `{"turnEndReason":"failure"}` {
		t.Fatalf("encoded data = %s, want the failure classification only", encoded)
	}
}

// TestACPFailureErrorCarriesTurnEndReasonInData 验证 session/prompt
// 失败路径增加了可选的 error.data.turnEndReason == "failure"，同时
// 错误 code 和 message 保持逐字节不变。
func TestACPFailureErrorCarriesTurnEndReasonInData(t *testing.T) {
	failure := errors.New("boom")
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: outcomeRunner{err: failure}})
	}()
	defer inW.Close()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new"})
	readJSON(t, dec)

	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "session/prompt",
		"params": map[string]any{"prompt": "hi"},
	})
	var resp map[string]any
	for {
		msg := readJSON(t, dec)
		if msg["id"] == float64(2) {
			resp = msg
			break
		}
	}
	if _, ok := resp["result"]; ok {
		t.Fatalf("failure unexpectedly returned a result: %v", resp)
	}
	body, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", resp)
	}
	if body["code"] != float64(codeInternalError) {
		t.Fatalf("error.code = %v, want %d", body["code"], codeInternalError)
	}
	if body["message"] != wireErrorText(failure) {
		t.Fatalf("error.message = %v, want unchanged %q", body["message"], wireErrorText(failure))
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("error.data missing on failure: %v", body)
	}
	if data["turnEndReason"] != string(TurnEndFailure) {
		t.Fatalf("error.data.turnEndReason = %v, want %q", data["turnEndReason"], TurnEndFailure)
	}
	_ = inW.Close()
	waitFor(t, done)
}

// TestACPNonPromptErrorHasNoTurnEndData 守护默认行为：
// 来自 session/prompt 以外方法的错误保持原有的两字段信封，
// 绝不会多出 data 成员。
func TestACPNonPromptErrorHasNoTurnEndData(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- RunACP(context.Background(), inR, outW, Config{Runner: outcomeRunner{}})
	}()
	defer inW.Close()

	enc := json.NewEncoder(inW)
	dec := json.NewDecoder(outR)
	_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/load"})
	resp := readJSON(t, dec)
	body, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", resp)
	}
	if _, present := body["data"]; present {
		t.Fatalf("non-prompt error gained a data field: %v", body)
	}
	_ = inW.Close()
	waitFor(t, done)
}
