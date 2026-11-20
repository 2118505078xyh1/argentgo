package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DeviceAuth is the result of starting an RFC 8628 device-code flow.
type DeviceAuth struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	// Interval is the polling interval in seconds.
	Interval int
	// ExpiresIn is the device code lifetime in seconds.
	ExpiresIn int
}

// ErrDeviceExpired reports that the device code (or the polling window) ran out.
var ErrDeviceExpired = errors.New("auth: device code expired")

// ErrDeviceDenied reports that the user declined the authorization.
var ErrDeviceDenied = errors.New("auth: authorization was denied")

const (
	deviceGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
	defaultDevicePoll   = 5 * time.Second
	defaultDeviceWindow = 15 * time.Minute
	deviceSlowDownStep  = 5 * time.Second
)

// deviceSleep and deviceNow are variables so tests can run the polling loop
// without real waits.
var (
	deviceSleep = sleepContext
	deviceNow   = time.Now
)

// DeviceStart asks the provider for a device code and a user code.
func DeviceStart(ctx context.Context, cfg Config) (DeviceAuth, error) {
	if cfg.DeviceURL == "" {
		return DeviceAuth{}, errors.New("auth: config has no device endpoint")
	}
	if cfg.ClientID == "" {
		return DeviceAuth{}, errors.New("auth: config has no client id")
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	if len(cfg.Scopes) > 0 {
		form.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if cfg.Audience != "" {
		form.Set("audience", cfg.Audience)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.DeviceURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("auth: build the device request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := cfg.httpClient().Do(request)
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("auth: device endpoint: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, tokenBodyLimit))
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("auth: read the device response: %w", err)
	}
	var parsed struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		ErrorDesc       string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return DeviceAuth{}, fmt.Errorf("auth: device endpoint returned HTTP %d with a non-JSON body",
			response.StatusCode)
	}
	if response.StatusCode/100 != 2 || parsed.Error != "" {
		return DeviceAuth{}, &tokenError{
			Status:      response.StatusCode,
			Code:        parsed.Error,
			Description: parsed.ErrorDesc,
		}
	}
	if parsed.DeviceCode == "" || parsed.UserCode == "" {
		return DeviceAuth{}, errors.New("auth: device endpoint returned no device_code/user_code")
	}
	auth := DeviceAuth{
		DeviceCode:      parsed.DeviceCode,
		UserCode:        parsed.UserCode,
		VerificationURI: parsed.VerificationURI,
		Interval:        parsed.Interval,
		ExpiresIn:       parsed.ExpiresIn,
	}
	if auth.Interval <= 0 {
		auth.Interval = int(defaultDevicePoll / time.Second)
	}
	if auth.ExpiresIn <= 0 {
		auth.ExpiresIn = int(defaultDeviceWindow / time.Second)
	}
	return auth, nil
}

// DevicePoll polls the token endpoint until the user approves, declines or the
// code expires. It honours authorization_pending and slow_down.
func DevicePoll(ctx context.Context, cfg Config, auth DeviceAuth) (Token, error) {
	if auth.DeviceCode == "" {
		return Token{}, errors.New("auth: missing device code")
	}
	interval := time.Duration(auth.Interval) * time.Second
	if auth.Interval <= 0 {
		interval = defaultDevicePoll
	}
	var deadline time.Time
	if auth.ExpiresIn > 0 {
		deadline = deviceNow().Add(time.Duration(auth.ExpiresIn) * time.Second)
	}
	for {
		if !deadline.IsZero() && deviceNow().After(deadline) {
			return Token{}, ErrDeviceExpired
		}
		response, err := deviceToken(ctx, cfg, auth.DeviceCode)
		decision := ClassifyDevicePoll(err, interval)
		switch decision.State {
		case DevicePollAuthorized:
			return tokenFromResponse(cfg, response, Token{})
		case DevicePollExpired:
			return Token{}, ErrDeviceExpired
		case DevicePollDenied:
			return Token{}, ErrDeviceDenied
		case DevicePollFailed:
			return Token{}, err
		}
		// DevicePollWaiting: adopt the (possibly lengthened) interval, then
		// wait before the next poll.
		interval = decision.Wait
		if err := deviceSleep(ctx, interval); err != nil {
			return Token{}, err
		}
	}
}

func deviceToken(ctx context.Context, cfg Config, deviceCode string) (tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", deviceGrantType)
	form.Set("device_code", deviceCode)
	form.Set("client_id", cfg.ClientID)
	return postToken(ctx, cfg, form)
}

// sleepContext waits for d but gives up as soon as ctx is cancelled.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
