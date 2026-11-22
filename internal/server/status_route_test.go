package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// routeSnapshot 把一次 HTTP 响应的状态码、响应头和响应体完整快照下来，
// 用于在测试中做逐字节比较（响应体不做任何解码或归一化）。
type routeSnapshot struct {
	code   int
	header http.Header
	body   []byte
}

func snapshotResponse(rec *httptest.ResponseRecorder) routeSnapshot {
	return routeSnapshot{
		code:   rec.Code,
		header: rec.Header().Clone(),
		body:   append([]byte(nil), rec.Body.Bytes()...),
	}
}

func assertSameResponse(t *testing.T, got, want routeSnapshot, label string) {
	t.Helper()
	if got.code != want.code {
		t.Errorf("%s: status = %d, want %d", label, got.code, want.code)
	}
	if !reflect.DeepEqual(got.header, want.header) {
		t.Errorf("%s: header = %v, want %v", label, got.header, want.header)
	}
	if !bytes.Equal(got.body, want.body) {
		t.Errorf("%s: body = %q, want %q (verbatim)", label, got.body, want.body)
	}
}

// TestStatusRouteDefaultNotFoundByteForByte 验证默认 Config 下 /v1/status
// 仍然得到 catch-all 的原有 404 字节：与任意未知路径的响应、以及与
// writeError 直接写出的参照响应三者逐字节一致。
func TestStatusRouteDefaultNotFoundByteForByte(t *testing.T) {
	h := NewHandler(Config{Token: "s3cret", Runner: &fakeRunner{}, Version: "1.2.3"})

	statusRec := httptest.NewRecorder()
	h.ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	got := snapshotResponse(statusRec)
	if got.code != http.StatusNotFound {
		t.Fatalf("/v1/status status = %d, want 404", got.code)
	}

	// 同一个处理器对未知路径的响应：证明 /v1/status 走的就是原有 catch-all。
	unknownRec := httptest.NewRecorder()
	h.ServeHTTP(unknownRec, httptest.NewRequest(http.MethodGet, "/v1/never-registered", nil))
	assertSameResponse(t, got, snapshotResponse(unknownRec), "status vs unknown path")

	// 参照：catch-all 调用的正是 writeError(404, "not found")。
	ref := httptest.NewRecorder()
	writeError(ref, http.StatusNotFound, "not found")
	assertSameResponse(t, got, snapshotResponse(ref), "status vs canonical 404")
}

// TestStatusRouteHealthVersionUnchanged 验证开启 StatusProvider 后，
// health 与 version 的响应字节完全不受影响（二者本来就不需要 token）。
func TestStatusRouteHealthVersionUnchanged(t *testing.T) {
	base := Config{Token: "s3cret", Runner: &fakeRunner{}, Version: "1.2.3"}
	withProvider := base
	withProvider.StatusProvider = staticStatusProvider([]byte(`{"状态":"正常"}`))

	baseHandler := NewHandler(base)
	optInHandler := NewHandler(withProvider)

	for _, path := range []string{"/v1/health", "/v1/version"} {
		t.Run(path, func(t *testing.T) {
			baseRec := httptest.NewRecorder()
			baseHandler.ServeHTTP(baseRec, httptest.NewRequest(http.MethodGet, path, nil))
			optInRec := httptest.NewRecorder()
			optInHandler.ServeHTTP(optInRec, httptest.NewRequest(http.MethodGet, path, nil))

			if baseRec.Code != http.StatusOK {
				t.Fatalf("base %s status = %d, want 200", path, baseRec.Code)
			}
			assertSameResponse(t, snapshotResponse(optInRec), snapshotResponse(baseRec), path+" with provider")
		})
	}
}

// TestStatusRouteOptInServesProviderBytes 验证 opt-in 后 GET /v1/status
// 逐字节返回提供者原始内容（含 CJK、结尾换行），且重复请求结果一致。
func TestStatusRouteOptInServesProviderBytes(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"ASCII without newline", []byte(`{"status":"ok","uptime":42}`)},
		{"CJK without newline", []byte(`{"状态":"正常","消息":"服务器运行良好，欢迎访问"}`)},
		{"body with trailing newline", []byte("{\"status\":\"ok\"}\n")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(Config{
				Token:          "s3cret",
				Runner:         &fakeRunner{},
				StatusProvider: staticStatusProvider(tt.body),
			})
			for i := 0; i < 3; i++ {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
				req.Header.Set("Authorization", "Bearer s3cret")
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("attempt %d: status = %d, want 200", i, rec.Code)
				}
				if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
					t.Errorf("attempt %d: Content-Type = %q, want application/json", i, ct)
				}
				if !bytes.Equal(rec.Body.Bytes(), tt.body) {
					t.Errorf("attempt %d: body = %q, want %q (verbatim)", i, rec.Body.Bytes(), tt.body)
				}
			}
		})
	}
}

// TestStatusRouteServesCapabilityBoardJSON 把 capability_status.go 的
// CapabilityBoard.JSON() 直接作为 /v1/status 的提供者字节，验证路由逐字节
// 透传能力目录（含 CJK 分类、无尾换行）：这是该原语"未来由 /v1/status 接入"
// 在 HTTP 侧的直接落点。
func TestStatusRouteServesCapabilityBoardJSON(t *testing.T) {
	var board CapabilityBoard
	board.Register("apply_patch", "工具", true)
	board.Register("grep_ex", "search", false)
	body := board.JSON()

	h := NewHandler(Config{
		Token:          "s3cret",
		Runner:         &fakeRunner{},
		StatusProvider: staticStatusProvider(body),
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("body = %q, want %q (verbatim)", rec.Body.Bytes(), body)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"工具"`)) {
		t.Fatalf("CJK 分类未原样透传: %q", rec.Body.Bytes())
	}
}

// TestStatusRouteAcceptsQueryToken 验证 /v1/status 与其它受保护端点一样，
// 可以通过 ?token= 提供凭据（沿用 h.auth / authorized 的既有逻辑）。
func TestStatusRouteAcceptsQueryToken(t *testing.T) {
	body := []byte(`{"status":"ok"}`)
	h := NewHandler(Config{
		Token:          "s3cret",
		Runner:         &fakeRunner{},
		StatusProvider: staticStatusProvider(body),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status?token=s3cret", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Errorf("body = %q, want %q", rec.Body.Bytes(), body)
	}
}

// TestStatusRouteRequiresToken 验证状态端点走 h.auth：无 token 时返回
// 既有的 401 字节（含 WWW-Authenticate 头），且不泄露任何状态内容。
func TestStatusRouteRequiresToken(t *testing.T) {
	h := NewHandler(Config{
		Token:          "s3cret",
		Runner:         &fakeRunner{},
		StatusProvider: staticStatusProvider([]byte(`{"secret":"internal"}`)),
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	got := snapshotResponse(rec)

	if got.code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got.code)
	}
	if auth := rec.Header().Get("WWW-Authenticate"); auth != `Bearer realm="argent"` {
		t.Errorf("WWW-Authenticate = %q, want Bearer realm=\"argent\"", auth)
	}
	if bytes.Contains(got.body, []byte("internal")) {
		t.Errorf("leaked status body on unauthorized request: %q", got.body)
	}
	ref := httptest.NewRecorder()
	ref.Header().Set("WWW-Authenticate", `Bearer realm="argent"`)
	writeError(ref, http.StatusUnauthorized, "unauthorized")
	assertSameResponse(t, got, snapshotResponse(ref), "unauthorized response")
}

// TestStatusRouteMethodNotAllowed 验证非 GET 方法复用既有的 405 响应
// （含 Allow: GET 头），且鉴权发生在方法检查之前：无 token 的 POST 仍是 401。
func TestStatusRouteMethodNotAllowed(t *testing.T) {
	h := NewHandler(Config{
		Token:          "s3cret",
		Runner:         &fakeRunner{},
		StatusProvider: staticStatusProvider([]byte(`{"status":"ok"}`)),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	h.ServeHTTP(rec, req)
	got := snapshotResponse(rec)
	if got.code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", got.code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow = %q, want GET", allow)
	}
	ref := httptest.NewRecorder()
	methodNotAllowed(ref, http.MethodGet)
	assertSameResponse(t, got, snapshotResponse(ref), "method not allowed")

	// 鉴权优先于方法检查：未带 token 的 POST 返回 401 而非 405。
	noToken := httptest.NewRecorder()
	h.ServeHTTP(noToken, httptest.NewRequest(http.MethodPost, "/v1/status", nil))
	if noToken.Code != http.StatusUnauthorized {
		t.Fatalf("POST without token status = %d, want 401", noToken.Code)
	}
}
