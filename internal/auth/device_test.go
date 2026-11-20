package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubDeviceSleep replaces the polling wait so tests never sleep for real.
func stubDeviceSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	original := deviceSleep
	waits := []time.Duration{}
	deviceSleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	t.Cleanup(func() { deviceSleep = original })
	return &waits
}

func TestDeviceStartParsesTheResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if !containsAll(string(body), "client_id=client", "scope=read%3Auser") {
			t.Errorf("device request body = %s", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		io.WriteString(writer, `{"device_code":"dev-1","user_code":"ABCD-EFGH",`+
			`"verification_uri":"https://github.com/login/device","expires_in":900,"interval":7}`)
	}))
	defer server.Close()

	cfg := Config{Provider: "github-copilot", ClientID: "client", DeviceURL: server.URL, Scopes: []string{"read:user"}}
	auth, err := DeviceStart(context.Background(), cfg)
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	want := DeviceAuth{
		DeviceCode:      "dev-1",
		UserCode:        "ABCD-EFGH",
		VerificationURI: "https://github.com/login/device",
		Interval:        7,
		ExpiresIn:       900,
	}
	if auth != want {
		t.Fatalf("DeviceAuth = %+v, want %+v", auth, want)
	}
}

func TestDeviceStartAppliesDefaultInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		io.WriteString(writer, `{"device_code":"d","user_code":"U","verification_uri":"https://x.invalid"}`)
	}))
	defer server.Close()

	auth, err := DeviceStart(context.Background(), Config{ClientID: "c", DeviceURL: server.URL})
	if err != nil {
		t.Fatalf("DeviceStart: %v", err)
	}
	if auth.Interval != 5 || auth.ExpiresIn != 900 {
		t.Fatalf("defaults not applied: %+v", auth)
	}
}

func TestDeviceStartRejectsIncompleteConfig(t *testing.T) {
	cases := []Config{
		{ClientID: "c"},
		{DeviceURL: "https://example.invalid/device"},
	}
	for _, cfg := range cases {
		if _, err := DeviceStart(context.Background(), cfg); err == nil {
			t.Fatalf("DeviceStart(%+v) succeeded, want error", cfg)
		}
	}
}

// deviceScript answers each token poll with the next scripted body.
func deviceScript(t *testing.T, bodies []string) *httptest.Server {
	t.Helper()
	index := 0
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if !containsAll(string(body), "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code",
			"device_code=dev-1") {
			t.Errorf("poll body = %s", body)
		}
		if index >= len(bodies) {
			t.Errorf("poll %d beyond the scripted responses", index)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		response := bodies[index]
		index++
		writer.Header().Set("Content-Type", "application/json")
		if containsAll(response, `"error"`) {
			writer.WriteHeader(http.StatusBadRequest)
		}
		io.WriteString(writer, response)
	}))
}

func TestDevicePollTransitions(t *testing.T) {
	cases := []struct {
		name       string
		responses  []string
		wantWaits  []time.Duration
		wantAccess string
		wantErr    error
	}{
		{
			name: "pending then success",
			responses: []string{
				`{"error":"authorization_pending"}`,
				`{"access_token":"tok-1","refresh_token":"ref-1","expires_in":3600}`,
			},
			wantWaits:  []time.Duration{2 * time.Second},
			wantAccess: "tok-1",
		},
		{
			name: "slow down adds five seconds",
			responses: []string{
				`{"error":"authorization_pending"}`,
				`{"error":"slow_down"}`,
				`{"access_token":"tok-2"}`,
			},
			wantWaits:  []time.Duration{2 * time.Second, 7 * time.Second},
			wantAccess: "tok-2",
		},
		{
			name:      "expired token",
			responses: []string{`{"error":"expired_token"}`},
			wantErr:   ErrDeviceExpired,
		},
		{
			name:      "access denied",
			responses: []string{`{"error":"access_denied"}`},
			wantErr:   ErrDeviceDenied,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			waits := stubDeviceSleep(t)
			server := deviceScript(t, test.responses)
			defer server.Close()

			cfg := Config{Provider: "github-copilot", ClientID: "client", TokenURL: server.URL}
			auth := DeviceAuth{DeviceCode: "dev-1", UserCode: "U", Interval: 2, ExpiresIn: 900}
			token, err := DevicePoll(context.Background(), cfg, auth)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DevicePoll: %v", err)
			}
			if token.Access != test.wantAccess || token.Provider != "github-copilot" {
				t.Fatalf("token = %+v", token)
			}
			if len(*waits) != len(test.wantWaits) {
				t.Fatalf("waits = %v, want %v", *waits, test.wantWaits)
			}
			for index, want := range test.wantWaits {
				if (*waits)[index] != want {
					t.Fatalf("wait %d = %v, want %v", index, (*waits)[index], want)
				}
			}
		})
	}
}

func TestDevicePollStopsWhenTheWindowCloses(t *testing.T) {
	stubDeviceSleep(t)
	original := deviceNow
	start := time.Unix(1_800_000_000, 0)
	calls := 0
	deviceNow = func() time.Time {
		calls++
		if calls == 1 {
			// The deadline is derived from this first reading.
			return start
		}
		return start.Add(time.Hour)
	}
	t.Cleanup(func() { deviceNow = original })

	cfg := Config{ClientID: "c", TokenURL: "http://127.0.0.1:1/token"}
	auth := DeviceAuth{DeviceCode: "dev-1", Interval: 1, ExpiresIn: 60}
	if _, err := DevicePoll(context.Background(), cfg, auth); !errors.Is(err, ErrDeviceExpired) {
		t.Fatalf("error = %v, want ErrDeviceExpired", err)
	}
}

func TestDevicePollRequiresADeviceCode(t *testing.T) {
	if _, err := DevicePoll(context.Background(), Config{}, DeviceAuth{}); err == nil {
		t.Fatal("DevicePoll accepted an empty device code")
	}
}

// TestDevicePollSendsClientSecretWhenConfigured covers the device-grant token
// request: a confidential client must authenticate its polls too.
func TestDevicePollSendsClientSecretWhenConfigured(t *testing.T) {
	cases := []struct {
		name       string
		secret     string
		wantSecret string
	}{
		{name: "public client", secret: "", wantSecret: ""},
		{name: "confidential client", secret: "device-secret", wantSecret: "device-secret"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			stubDeviceSleep(t)
			var bodies []string
			index := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				bodies = append(bodies, string(body))
				writer.Header().Set("Content-Type", "application/json")
				if index == 0 {
					index++
					writer.WriteHeader(http.StatusBadRequest)
					io.WriteString(writer, `{"error":"authorization_pending"}`)
					return
				}
				io.WriteString(writer, `{"access_token":"tok"}`)
			}))
			defer server.Close()

			cfg := Config{Provider: "p", ClientID: "client", ClientSecret: test.secret, TokenURL: server.URL}
			auth := DeviceAuth{DeviceCode: "dev-1", Interval: 1, ExpiresIn: 900}
			token, err := DevicePoll(context.Background(), cfg, auth)
			if err != nil || token.Access != "tok" {
				t.Fatalf("DevicePoll = %+v, %v", token, err)
			}
			if len(bodies) != 2 {
				t.Fatalf("got %d polls, want 2", len(bodies))
			}
			for i, body := range bodies {
				if test.wantSecret == "" {
					if strings.Contains(body, "client_secret=") {
						t.Fatalf("poll %d sent an empty client_secret: %s", i, body)
					}
				} else if !containsAll(body, "client_secret="+test.wantSecret) {
					t.Fatalf("poll %d body = %s, want client_secret=%s", i, body, test.wantSecret)
				}
			}
		})
	}
}

// containsAll asserts every fragment occurs in a request body.
func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
