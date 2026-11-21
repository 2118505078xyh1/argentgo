package hooks

import (
	"errors"
	"testing"
)

var errSourceRead = errors.New("boom: source read failed")

// keyedSourceFor 用 NewKeyedSource + SourceFunc 构造一个测试用 Source，
// 借此同时覆盖组合类型与函数适配器的行为。
func keyedSourceFor(key string, data []byte, readErr error) Source {
	return NewKeyedSource(key, SourceFunc(func() ([]byte, error) {
		if readErr != nil {
			return nil, readErr
		}
		return data, nil
	}))
}

func TestLoadSource(t *testing.T) {
	const valid = trustedDocument

	tests := []struct {
		name         string
		key          string
		data         []byte
		readErr      error
		trustKey     string // 非空时按 Digest(data) 授予该键
		nilTrust     bool
		wantErrIs    error
		wantParseErr bool
		wantCommands int
	}{
		{
			name:         "合法源加载",
			key:          "hooks/valid.json",
			data:         []byte(valid),
			trustKey:     "hooks/valid.json",
			wantCommands: 1,
		},
		{
			name:      "未授信摘要拒绝",
			key:       "hooks/untrusted.json",
			data:      []byte(valid),
			trustKey:  "", // 不授予
			wantErrIs: ErrUntrusted,
		},
		{
			name:      "读失败透传",
			key:       "hooks/read-error.json",
			readErr:   errSourceRead,
			trustKey:  "hooks/read-error.json",
			wantErrIs: errSourceRead,
		},
		{
			name:         "解析失败",
			key:          "hooks/parse-error.json",
			data:         []byte("not-json"),
			trustKey:     "hooks/parse-error.json",
			wantParseErr: true,
		},
		{
			name:      "先信任后解析：未授信的坏 JSON 报未授信",
			key:       "hooks/order.json",
			data:      []byte("{bad json"),
			trustKey:  "", // 未授信，且内容也无法解析
			wantErrIs: ErrUntrusted,
		},
		{
			name:         "CJK Key",
			key:          "配置/钩子.json",
			data:         []byte(valid),
			trustKey:     `配置\钩子.json`, // 分隔符差异应被归一化
			wantCommands: 1,
		},
		{
			name:      "nil 信任存储拒绝",
			key:       "hooks/nil-trust.json",
			data:      []byte(valid),
			nilTrust:  true,
			wantErrIs: ErrUntrusted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewTrustStore()
			if !tt.nilTrust && tt.trustKey != "" {
				store.Trust(tt.trustKey, Digest(tt.data))
			}
			var trust *TrustStore = store
			if tt.nilTrust {
				trust = nil
			}

			doc, err := LoadSource(keyedSourceFor(tt.key, tt.data, tt.readErr), trust)

			if tt.wantErrIs != nil {
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("error = %v, want errors.Is(_, %v)", err, tt.wantErrIs)
				}
				if doc != nil {
					t.Fatalf("expected nil document on error, got %+v", doc)
				}
				return
			}
			if tt.wantParseErr {
				if err == nil {
					t.Fatal("expected a parse error")
				}
				if errors.Is(err, ErrUntrusted) {
					t.Fatalf("trusted content should reach the parser, got ErrUntrusted: %v", err)
				}
				if doc != nil {
					t.Fatalf("expected nil document on parse error, got %+v", doc)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadSource: %v", err)
			}
			if doc == nil || doc.Config == nil {
				t.Fatalf("expected a loaded document, got %+v", doc)
			}
			if doc.Path != tt.key {
				t.Fatalf("Path = %q, want %q", doc.Path, tt.key)
			}
			if doc.Digest != Digest(tt.data) {
				t.Fatalf("Digest = %q, want %q", doc.Digest, Digest(tt.data))
			}
			if len(doc.Config.Commands) != tt.wantCommands {
				t.Fatalf("commands = %d, want %d", len(doc.Config.Commands), tt.wantCommands)
			}
		})
	}
}

func TestLoadSourceTrustGatePrecedesParse(t *testing.T) {
	// 同一份无法解析的字节：未授信时必须在解析之前就被拒绝；授信后才暴露解析错误。
	bad := []byte("{not valid json")

	_, err := LoadSource(keyedSourceFor("hooks/gate.json", bad, nil), NewTrustStore())
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("untrusted bad bytes should report ErrUntrusted, got %v", err)
	}

	store := NewTrustStore()
	store.Trust("hooks/gate.json", Digest(bad))
	_, err = LoadSource(keyedSourceFor("hooks/gate.json", bad, nil), store)
	if err == nil || errors.Is(err, ErrUntrusted) {
		t.Fatalf("trusted bad bytes should report a parse error, got %v", err)
	}
}

func TestSourceFuncNeverBypassesTrust(t *testing.T) {
	// SourceFunc 只是字节来源；即便它返回合法文档，空信任存储仍必须拒绝。
	fn := SourceFunc(func() ([]byte, error) { return []byte(trustedDocument), nil })
	src := NewKeyedSource("hooks/fn.json", fn)

	if src.Key() != "hooks/fn.json" {
		t.Fatalf("Key = %q", src.Key())
	}
	if _, err := src.Bytes(); err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if _, err := LoadSource(src, NewTrustStore()); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("SourceFunc must not bypass the trust gate: %v", err)
	}
}
