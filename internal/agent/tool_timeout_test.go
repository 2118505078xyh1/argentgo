package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestToolTimeoutPolicy(t *testing.T) {
	t.Setenv("ARGENT_TOOL_TIMEOUT", "1")
	if got := toolTimeoutFor("bash"); got != 120*time.Second {
		t.Fatalf("bash timeout = %v, want 120s", got)
	}
	if got := toolTimeoutFor("fetch"); got != 20*time.Second {
		t.Fatalf("fetch timeout = %v, want 20s", got)
	}
	if got := toolTimeoutFor("unknown"); got != 0 {
		t.Fatalf("unknown timeout = %v, want 0", got)
	}

	// 自己的截止时间胜出：返回带 code 的结构化错误。
	callCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-callCtx.Done()
	err, ok := normaliseTimeoutError(errors.New("boom"), callCtx, context.Background(), "bash", 120*time.Second)
	if !ok || !errors.Is(err, ErrToolTimeout) {
		t.Fatalf("自身超时未规范化: err=%v ok=%v", err, ok)
	}
	if !strings.Contains(err.Error(), "TOOL_TIMEOUT") || !strings.Contains(err.Error(), "bash") {
		t.Fatalf("错误文案缺少 code: %s", err.Error())
	}

	// 外层 ctx 先取消：保持原始错误，不误归因。
	outer, outerCancel := context.WithCancel(context.Background())
	outerCancel()
	callCtx2, cancel2 := context.WithTimeout(outer, time.Millisecond)
	defer cancel2()
	original := errors.New("outer cancelled")
	err, ok = normaliseTimeoutError(original, callCtx2, outer, "bash", 120*time.Second)
	if ok || !errors.Is(err, original) || errors.Is(err, ErrToolTimeout) {
		t.Fatalf("外层超时被误归因: err=%v ok=%v", err, ok)
	}
}

func TestToolTimeoutEnvFallback(t *testing.T) {
	t.Setenv("ARGENT_TOOL_TIMEOUT", "0")
	if !timeoutDisabledByEnv() {
		t.Fatal("=0 应禁用超时包裹")
	}
	ctx, cancel, limit := withToolTimeout(context.Background(), "bash")
	defer cancel()
	if limit != 0 || ctx.Err() != nil {
		t.Fatalf("禁用时不应派生截止时间: limit=%v err=%v", limit, ctx.Err())
	}
}
