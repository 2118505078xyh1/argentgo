package server

import (
	"net/http"
	"testing"
)

func TestParseHealthResponse(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		body        string
		wantStatus  string
		wantVersion string
	}{
		{
			name:        "200 with version",
			statusCode:  http.StatusOK,
			body:        `{"status":"ok","version":"1.2.3"}`,
			wantStatus:  HealthOK,
			wantVersion: "1.2.3",
		},
		{
			name:       "503 with shutting down means closing",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"status":"closing","error":"server is shutting down"}`,
			wantStatus: HealthClosing,
		},
		{
			name:       "503 without shutting down is unhealthy",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"status":"closing","error":"server overloaded"}`,
			wantStatus: HealthUnhealthy,
		},
		{
			name:       "500 is unhealthy",
			statusCode: http.StatusInternalServerError,
			body:       `{"error":"boom"}`,
			wantStatus: HealthUnhealthy,
		},
		{
			name:       "invalid JSON falls back to status code",
			statusCode: http.StatusOK,
			body:       `not-json`,
			wantStatus: HealthOK,
		},
		{
			name:       "invalid JSON 503 still detects shutting down",
			statusCode: http.StatusServiceUnavailable,
			body:       `garbage shutting down garbage`,
			wantStatus: HealthClosing,
		},
		{
			name:       "missing fields keep status and empty version",
			statusCode: http.StatusOK,
			body:       `{}`,
			wantStatus: HealthOK,
		},
		{
			name:        "CJK version extracted intact",
			statusCode:  http.StatusOK,
			body:        `{"status":"ok","version":"版本一·测试"}`,
			wantStatus:  HealthOK,
			wantVersion: "版本一·测试",
		},
		{
			name:       "CJK error does not break shutdown detection",
			statusCode: http.StatusServiceUnavailable,
			body:       `{"status":"closing","error":"服务器正在关闭 shutting down","note":"备注"}`,
			wantStatus: HealthClosing,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseHealthResponse(tt.statusCode, []byte(tt.body))
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVersion)
			}
		})
	}
}
