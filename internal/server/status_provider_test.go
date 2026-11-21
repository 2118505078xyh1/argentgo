package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// staticStatusProvider 是一个固定的测试提供者，返回预先给定的字节。
// 即便字节为 nil，它本身仍是非 nil 的提供者，用于验证“存在但内容为空”。
type staticStatusProvider []byte

func (s staticStatusProvider) StatusJSON() []byte { return []byte(s) }

func TestStatusMuxStatusJSON(t *testing.T) {
	cjk := []byte(`{"状态":"正常","消息":"服务器运行良好，欢迎访问"}`)
	tests := []struct {
		name     string
		register StatusProvider
		wantOK   bool
		wantBody []byte
	}{
		{
			name:     "no provider returns nil and false",
			wantOK:   false,
			wantBody: nil,
		},
		{
			name:     "ASCII body returned verbatim",
			register: staticStatusProvider([]byte(`{"status":"ok"}`)),
			wantOK:   true,
			wantBody: []byte(`{"status":"ok"}`),
		},
		{
			name:     "CJK body returned verbatim",
			register: staticStatusProvider(cjk),
			wantOK:   true,
			wantBody: cjk,
		},
		{
			name:     "registered but empty body still reports present",
			register: staticStatusProvider(nil),
			wantOK:   true,
			wantBody: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mux StatusMux
			if tt.register != nil {
				mux.RegisterStatus(tt.register)
			}
			body, ok := mux.StatusJSON()
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !bytes.Equal(body, tt.wantBody) {
				t.Fatalf("body = %q, want %q (verbatim)", body, tt.wantBody)
			}
		})
	}
}

func TestStatusMuxRegisterNilClearsProvider(t *testing.T) {
	var mux StatusMux
	mux.RegisterStatus(staticStatusProvider([]byte(`{"status":"ok"}`)))
	if _, ok := mux.StatusJSON(); !ok {
		t.Fatal("provider should be present after RegisterStatus")
	}

	mux.RegisterStatus(nil)
	body, ok := mux.StatusJSON()
	if ok || body != nil {
		t.Fatalf("StatusJSON = (%q, %v), want (nil, false) after nil registration", body, ok)
	}
}

func TestWriteStatus(t *testing.T) {
	body := []byte(`{"status":"ok","uptime":42}`)
	cjk := []byte(`{"消息":"你好，世界","状态":"正常"}`)
	tests := []struct {
		name        string
		method      string
		mux         *StatusMux
		register    StatusProvider
		wantHandled bool
		wantBody    []byte
	}{
		{
			name:        "GET with provider writes bytes verbatim",
			method:      http.MethodGet,
			register:    staticStatusProvider(body),
			wantHandled: true,
			wantBody:    body,
		},
		{
			name:        "GET with CJK provider writes bytes verbatim",
			method:      http.MethodGet,
			register:    staticStatusProvider(cjk),
			wantHandled: true,
			wantBody:    cjk,
		},
		{
			name:        "GET without provider is not handled",
			method:      http.MethodGet,
			wantHandled: false,
		},
		{
			name:        "POST with provider is not handled",
			method:      http.MethodPost,
			register:    staticStatusProvider(body),
			wantHandled: false,
		},
		{
			name:        "HEAD with provider is not handled",
			method:      http.MethodHead,
			register:    staticStatusProvider(body),
			wantHandled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := tt.mux
			if mux == nil {
				mux = &StatusMux{}
			}
			if tt.register != nil {
				mux.RegisterStatus(tt.register)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/v1/status", nil)
			handled := WriteStatus(rec, req, mux)
			if handled != tt.wantHandled {
				t.Fatalf("handled = %v, want %v", handled, tt.wantHandled)
			}

			if !tt.wantHandled {
				// 未处理时必须完全不触碰 w，交给调用方走既有 404/405 分支。
				if rec.Body.Len() != 0 {
					t.Errorf("body = %q, want untouched", rec.Body.Bytes())
				}
				if ct := rec.Header().Get("Content-Type"); ct != "" {
					t.Errorf("Content-Type = %q, want untouched", ct)
				}
				if allow := rec.Header().Get("Allow"); allow != "" {
					t.Errorf("Allow = %q, want untouched", allow)
				}
				return
			}

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if !bytes.Equal(rec.Body.Bytes(), tt.wantBody) {
				t.Errorf("body = %q, want %q (verbatim)", rec.Body.Bytes(), tt.wantBody)
			}
		})
	}
}

func TestWriteStatusNilMux(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	if WriteStatus(rec, req, nil) {
		t.Fatal("nil mux should not be handled")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want untouched", rec.Body.Bytes())
	}
}

func TestStatusMuxConcurrentRegisterAndRead(t *testing.T) {
	var mux StatusMux
	const (
		readers    = 8
		iterations = 200
	)

	var wg sync.WaitGroup
	// 单一写者，使最终状态确定：提供者始终返回 1 个字节。
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			mux.RegisterStatus(staticStatusProvider([]byte{byte(i + 1)}))
		}
	}()

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				body, ok := mux.StatusJSON()
				if ok && len(body) != 1 {
					t.Errorf("StatusJSON body = %q, want 1 byte", body)
				}

				// 同时压测完整 HTTP 路径；每个 goroutine 用独立 recorder。
				rec := httptest.NewRecorder()
				if handled := WriteStatus(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil), &mux); handled {
					if rec.Body.Len() != 1 {
						t.Errorf("written body = %q, want 1 byte", rec.Body.Bytes())
					}
				}
			}
		}()
	}
	wg.Wait()
}
