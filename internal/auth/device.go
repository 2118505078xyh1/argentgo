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

// DeviceAuth 是启动 RFC 8628 设备码流程的结果。
type DeviceAuth struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	// Interval 是以秒为单位的轮询间隔。
	Interval int
	// ExpiresIn 是设备码的生命周期秒数。
	ExpiresIn int
}

// ErrDeviceExpired 报告设备码（或轮询窗口）已用尽。
var ErrDeviceExpired = errors.New("auth: device code expired")

// ErrDeviceDenied 报告用户拒绝了授权。
var ErrDeviceDenied = errors.New("auth: authorization was denied")

const (
	deviceGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
	defaultDevicePoll   = 5 * time.Second
	defaultDeviceWindow = 15 * time.Minute
	deviceSlowDownStep  = 5 * time.Second
)

// deviceSleep 和 deviceNow 是变量，使测试能运行轮询循环而无需真实等待。
// 而无需真实等待。
var (
	deviceSleep = sleepContext
	deviceNow   = time.Now
)

// DeviceStart 向 provider 请求设备码和用户码。
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

// DevicePoll 轮询令牌端点直到用户批准、拒绝或
// DevicePoll 轮询令牌端点直到用户批准、拒绝或码过期。它遵守 authorization_pending 和 slow_down。
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
		// DevicePollWaiting：采用（可能已延长的）间隔，然后在下次轮询前
		// 等待。
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

// sleepContext 等待 d，但一旦 ctx 被取消就放弃。
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
