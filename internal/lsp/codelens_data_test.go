package lsp

import (
	"encoding/json"
	"testing"
)

func TestResolveCodeLensPreservesData(t *testing.T) {
	const data = `{"target":"file:///x.go#12"}`
	var gotParams json.RawMessage
	client := newIDEFakeClient(t, func(method string, params json.RawMessage) (any, *rpcError) {
		if method == "codeLens/resolve" {
			gotParams = params
			// 响应只给 command，省略 data —— 客户端应从请求 lens 继承。
			return map[string]any{
				"range":   ideRange(1, 2, 3, 4),
				"command": map[string]any{"title": "Run", "command": "go.test"},
			}, nil
		}
		return nil, nil
	})
	ctx := ideCtx(t)
	lens := CodeLens{Range: ideRange(1, 2, 3, 4), Data: json.RawMessage(data)}
	resolved, err := client.ResolveCodeLens(ctx, lens)
	if err != nil || resolved == nil {
		t.Fatalf("ResolveCodeLens: %v / %v", resolved, err)
	}

	// resolve 参数必须把 data 原样发给服务器。
	var sent struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(gotParams, &sent); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if string(sent.Data) != data {
		t.Fatalf("resolve data not forwarded: got %s, want %s", sent.Data, data)
	}
	// 响应省略 data 时继承请求 lens 的 data。
	if string(resolved.Data) != data {
		t.Fatalf("resolved data not inherited: got %s", resolved.Data)
	}
	// 响应带 command ⇒ IsResolved 为真（依据响应推导，不再虚假标记）。
	if !resolved.IsResolved {
		t.Fatal("resolved lens with command should be IsResolved")
	}
}

func TestResolveCodeLensNoCommandNotResolved(t *testing.T) {
	client := newIDEFakeClient(t, func(method string, params json.RawMessage) (any, *rpcError) {
		return map[string]any{"range": ideRange(1, 2, 3, 4)}, nil // 无 command
	})
	resolved, err := client.ResolveCodeLens(ideCtx(t), CodeLens{Range: ideRange(1, 2, 3, 4)})
	if err != nil || resolved == nil {
		t.Fatalf("ResolveCodeLens: %v / %v", resolved, err)
	}
	if resolved.IsResolved {
		t.Fatal("lens without command must not be marked resolved")
	}
}
