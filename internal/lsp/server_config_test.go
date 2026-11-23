package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/config"
)

// TestConfiguredServerInitializationOptions 锁定 #43 的默认路径行为：
// 配置里的 initialization 经 NewRegistryFromConfig 注入 Server，并随 initialize
// 请求发送；缺省时请求体不含 initializationOptions（逐字节不变）。
func TestConfiguredServerInitializationOptions(t *testing.T) {
	init := map[string]any{
		"python": map[string]any{
			"analysis": map[string]any{"extraPaths": []any{"/workspace/lib"}},
		},
	}
	cfg := config.LSPConfig{Servers: map[string]config.LSPServer{
		"pyright-custom": {
			Command:        []string{"fake-pyright", "--stdio"},
			Extensions:     []string{"py"},
			Env:            map[string]string{"PYRIGHT_PYTHON_FORCE_VERSION": "latest"},
			Initialization: init,
		},
	}}
	reg := NewRegistryFromConfig(cfg)
	server, ok := reg.Lookup("python")
	if !ok {
		t.Fatal("configured extension py should map to language python")
	}
	if server.Command != "fake-pyright" || len(server.Args) != 1 || server.Args[0] != "--stdio" {
		t.Fatalf("command/args not applied: %+v", server)
	}
	if !reflect.DeepEqual(server.InitializationOptions, init) {
		t.Fatalf("initialization options not injected: %#v", server.InitializationOptions)
	}
	if len(server.Env) != 1 || server.Env[0] != "PYRIGHT_PYTHON_FORCE_VERSION=latest" {
		t.Fatalf("env not applied: %#v", server.Env)
	}

	// 用 io.Pipe 捕获客户端实际发出的 initialize 请求体。
	clientRead, serverWrite := io.Pipe()
	serverRead, clientWrite := io.Pipe()
	seen := make(chan map[string]any, 1)
	go func() {
		defer serverWrite.Close()
		reader := bufio.NewReader(serverRead)
		for {
			payload, err := readFrame(reader)
			if err != nil {
				return
			}
			var msg struct {
				Method string          `json:"method"`
				Params map[string]any  `json:"params"`
				ID     json.RawMessage `json:"id"`
			}
			if json.Unmarshal(payload, &msg) != nil {
				continue
			}
			switch msg.Method {
			case "initialize":
				seen <- msg.Params
				reply, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg.ID,
					"result":  map[string]any{"capabilities": map[string]any{}},
				})
				_ = writeFrame(serverWrite, reply)
			case "shutdown":
				reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": nil})
				_ = writeFrame(serverWrite, reply)
			case "exit":
				return
			}
		}
	}()

	client := newClient(server, t.TempDir(), clientWrite, clientRead)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	select {
	case params := <-seen:
		got, ok := params["initializationOptions"]
		if !ok {
			t.Fatal("initialize params missing initializationOptions")
		}
		if !reflect.DeepEqual(got, init) {
			t.Fatalf("initializationOptions mismatch: got %#v want %#v", got, init)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initialize params")
	}
	_ = client.Close()
}

// TestConfiguredServerDefaultUnchanged 锁定：空配置产出的注册表与内置默认一致，
// 且默认 Server 没有 InitializationOptions（initialize 请求体不变）。
func TestConfiguredServerDefaultUnchanged(t *testing.T) {
	want := NewRegistry()
	got := NewRegistryFromConfig(config.LSPConfig{})
	if !reflect.DeepEqual(want.Names(), got.Names()) {
		t.Fatalf("registry names changed: got %v want %v", got.Names(), want.Names())
	}
	for _, server := range got.Servers() {
		if server.InitializationOptions != nil {
			t.Fatalf("default server %s has init options", server.Name)
		}
	}
}

// TestConfiguredServerDisabledAndTrustSource 锁定 disabled 与来源门控所需的数据。
func TestConfiguredServerDisabledAndTrustSource(t *testing.T) {
	cfg := config.LSPConfig{
		Servers: map[string]config.LSPServer{
			"gopls": {Disabled: true},
			"pyright": {
				Command:        []string{"pyright-langserver", "--stdio"},
				Extensions:     []string{"py"},
				Initialization: map[string]any{"x": true},
			},
		},
	}
	reg := NewRegistryFromConfig(cfg)
	if _, ok := reg.Lookup("go"); ok {
		t.Fatal("disabled gopls should be removed")
	}
	if _, ok := reg.Lookup("python"); !ok {
		t.Fatal("pyright should still be registered")
	}
	if cfg.Disabled {
		t.Fatal("unexpected global disable")
	}
}
