package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDeviceFlowEndToEnd 把 /device 与 /token 两个端点串起来验证完整的设备码
// 流程：用户码（含中文）正确解析、pending 之后拿到令牌、等待被接缝替换。
func TestDeviceFlowEndToEnd(t *testing.T) {
	waits := stubDeviceSleep(t)
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/device":
			if request.Method != http.MethodPost {
				t.Errorf("device method = %s, want POST", request.Method)
			}
			io.WriteString(writer, `{"device_code":"dev-e2e","user_code":"中文-代码",`+
				`"verification_uri":"https://example.com/验证","expires_in":900,"interval":1}`)
		case "/token":
			polls++
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), "device_code=dev-e2e") {
				t.Errorf("token body = %s", body)
			}
			if polls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				io.WriteString(writer, `{"error":"authorization_pending"}`)
				return
			}
			io.WriteString(writer, `{"access_token":"tok-e2e","refresh_token":"ref-e2e","expires_in":3600}`)
		default:
			t.Errorf("unexpected path %q", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cfg := Config{
		Provider:  "github-copilot",
		ClientID:  "client",
		DeviceURL: server.URL + "/device",
		TokenURL:  server.URL + "/token",
		Scopes:    []string{"read:user"},
	}
	started, err := DeviceStart(context.Background(), cfg)
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if started.UserCode != "中文-代码" || started.VerificationURI != "https://example.com/验证" {
		t.Fatalf("CJK user code / URI lost: %+v", started)
	}
	if started.Interval != 1 || started.ExpiresIn != 900 {
		t.Fatalf("parsed auth = %+v", started)
	}

	token, err := DevicePoll(context.Background(), cfg, started)
	if err != nil {
		t.Fatalf("DevicePoll: %v", err)
	}
	if token.Access != "tok-e2e" || token.Refresh != "ref-e2e" ||
		token.Kind != KindOAuth || token.Provider != "github-copilot" {
		t.Fatalf("token = %+v", token)
	}
	if polls != 2 {
		t.Fatalf("polls = %d, want 2", polls)
	}
	if len(*waits) != 1 || (*waits)[0] != time.Second {
		t.Fatalf("waits = %v, want [1s]", *waits)
	}
}
