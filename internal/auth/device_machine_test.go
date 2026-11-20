package auth

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestClassifyDevicePoll(t *testing.T) {
	const interval = 2 * time.Second
	transportErr := errors.New("network down")

	cases := []struct {
		name    string
		err     error
		want    DevicePollState
		wantMin time.Duration // only meaningful for DevicePollWaiting
	}{
		{"success is authorized", nil, DevicePollAuthorized, 0},
		{"pending keeps waiting", &tokenError{Code: "authorization_pending", Status: http.StatusBadRequest}, DevicePollWaiting, interval},
		{"slow down keeps waiting", &tokenError{Code: "slow_down", Status: http.StatusBadRequest}, DevicePollWaiting, interval + deviceSlowDownStep},
		{"expired token is terminal", &tokenError{Code: "expired_token", Status: http.StatusBadRequest}, DevicePollExpired, 0},
		{"access denied is terminal", &tokenError{Code: "access_denied", Status: http.StatusBadRequest}, DevicePollDenied, 0},
		{"unknown code fails", &tokenError{Code: "weird_error", Status: http.StatusBadRequest}, DevicePollFailed, 0},
		{"empty code fails", &tokenError{Status: http.StatusBadRequest}, DevicePollFailed, 0},
		{"transport error fails", transportErr, DevicePollFailed, 0},
		{"wrapped pending is recognized", fmt.Errorf("poll: %w", &tokenError{Code: "authorization_pending"}), DevicePollWaiting, interval},
		{"wrapped slow down still lengthens", fmt.Errorf("poll: %w", &tokenError{Code: "slow_down"}), DevicePollWaiting, interval + deviceSlowDownStep},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := ClassifyDevicePoll(test.err, interval)
			if got.State != test.want {
				t.Fatalf("ClassifyDevicePoll(%v) state = %q, want %q", test.err, got.State, test.want)
			}
			if test.want == DevicePollWaiting {
				if got.Wait != test.wantMin {
					t.Fatalf("wait = %v, want %v", got.Wait, test.wantMin)
				}
			} else if got.Wait != 0 {
				t.Fatalf("terminal/authorized state %q must have zero wait, got %v", got.State, got.Wait)
			}
		})
	}
}

func TestClassifyDevicePollSlowDownIsCumulative(t *testing.T) {
	// 把每个决定的 Wait 反馈回去：两次 slow_down 响应使间隔延长两次，这正是 DevicePoll 持续退避的方式。
	// 间隔两次，这正是 DevicePoll 持续退避的方式。
	interval := 2 * time.Second
	for i := 0; i < 2; i++ {
		decision := ClassifyDevicePoll(&tokenError{Code: "slow_down"}, interval)
		if decision.State != DevicePollWaiting {
			t.Fatalf("slow_down state = %q, want waiting", decision.State)
		}
		interval = decision.Wait
	}
	if interval != 2*time.Second+2*deviceSlowDownStep {
		t.Fatalf("interval after two slow_downs = %v, want %v", interval, 2*time.Second+2*deviceSlowDownStep)
	}
}

func TestDevicePollStateString(t *testing.T) {
	cases := map[DevicePollState]string{
		DevicePollAuthorized: "authorized",
		DevicePollWaiting:    "waiting",
		DevicePollExpired:    "expired",
		DevicePollDenied:     "denied",
		DevicePollFailed:     "failed",
	}
	for state, want := range cases {
		if got := state.String(); got != want {
			t.Fatalf("String = %q, want %q", got, want)
		}
	}
}
