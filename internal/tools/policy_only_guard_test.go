package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPolicyOnlyGuardCheck(t *testing.T) {
	sentinel := errors.New("policy denied")

	tests := []struct {
		name        string
		policy      func(string, json.RawMessage) error
		toolName    string
		args        json.RawMessage
		wantErr     error
		wantInvoked bool
	}{
		{
			name:     "nil 谓词放行且不调用",
			toolName: "read",
			args:     json.RawMessage(`{"path":"secret"}`),
		},
		{
			name:        "拒绝谓词透传哨兵错误",
			policy:      func(string, json.RawMessage) error { return sentinel },
			toolName:    "read",
			args:        json.RawMessage(`{"path":"secret"}`),
			wantErr:     sentinel,
			wantInvoked: true,
		},
		{
			name:        "放行谓词返回 nil（CJK 工具名原样传入）",
			policy:      func(string, json.RawMessage) error { return nil },
			toolName:    "读取文件",
			args:        json.RawMessage(`{"路径":"值"}`),
			wantInvoked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append(json.RawMessage(nil), tt.args...)

			// 用包装谓词记录 Check 实际转发的入参，同时保留被测谓词行为。
			var gotName string
			var gotArgs json.RawMessage
			invoked := false
			var wrapped func(string, json.RawMessage) error
			if tt.policy != nil {
				wrapped = func(name string, args json.RawMessage) error {
					invoked = true
					gotName = name
					gotArgs = append(json.RawMessage(nil), args...)
					return tt.policy(name, args)
				}
			}

			g := NewPolicyOnlyGuard(wrapped)
			err := g.Check(context.Background(), tt.toolName, tt.args)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Check 错误 = %v, want %v", err, tt.wantErr)
			}
			if invoked != tt.wantInvoked {
				t.Fatalf("谓词调用 = %v, want %v", invoked, tt.wantInvoked)
			}
			if tt.wantInvoked {
				if gotName != tt.toolName {
					t.Errorf("谓词收到工具名 = %q, want %q", gotName, tt.toolName)
				}
				if !bytes.Equal(gotArgs, tt.args) {
					t.Errorf("谓词收到 args = %s, want %s", gotArgs, tt.args)
				}
			}
			// Check 不得修改入参（纯转发）。
			if !bytes.Equal(tt.args, original) {
				t.Errorf("Check 修改了入参：got %s, want %s", tt.args, original)
			}
		})
	}
}

func TestPolicyOnlyGuardConcurrentCheck(t *testing.T) {
	var calls atomic.Int64
	g := NewPolicyOnlyGuard(func(name string, args json.RawMessage) error {
		calls.Add(1)
		if name != "并发工具" || !bytes.Equal(args, json.RawMessage(`{}`)) {
			t.Errorf("谓词收到 name=%q args=%s", name, args)
		}
		return nil
	})

	const goroutines = 32
	const iterations = 100
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if err := g.Check(context.Background(), "并发工具", json.RawMessage(`{}`)); err != nil {
					t.Errorf("并发 Check 错误 = %v, want nil", err)
				}
			}
		}()
	}
	wg.Wait()

	if got := calls.Load(); got != goroutines*iterations {
		t.Fatalf("谓词调用次数 = %d, want %d", got, goroutines*iterations)
	}
}
