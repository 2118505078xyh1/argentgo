package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config 描述一个 provider 的 OAuth 端点和客户端身份。本包中的预设填充它；自定义 provider 可以设置这些字段。
// 本包中的预设填充它；自定义 provider 可以设置
// directly.
type Config struct {
	// Provider 是与令牌一起存储的 id（例如 "openai"）。
	Provider string
	// ClientID 是公开的 OAuth 客户端标识。
	ClientID string
	// ClientSecret 仅非公开客户端的 provider 需要。
	ClientSecret string
	// AuthorizeURL 是浏览器授权端点。
	AuthorizeURL string
	// TokenURL 交换 code 和刷新令牌。
	TokenURL string
	// DeviceURL 启动设备码流程（RFC 8628）。
	DeviceURL string
	// RedirectURI 必须匹配用于回调的 loopback 监听器。
	RedirectURI string
	// Scopes 在请求中用空格连接。
	Scopes []string
	// Audience 是可选的额外授权参数（Auth0 风格）。
	Audience string
	// Extra 添加 provider 专属的授权参数。
	Extra map[string]string
	// HTTPClient 覆盖默认客户端（测试中有用）。
	HTTPClient *http.Client
}

const tokenBodyLimit = 1 << 20

// PKCE 返回一个新的 RFC 7636 verifier 及其 S256 challenge。
func PKCE() (verifier, challenge string, err error) {
	buf := make([]byte, 48)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("auth: generate the PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	return verifier, S256(verifier), nil
}

// S256 是 verifier 的 base64url 编码 SHA-256，即 code_challenge 值。
func S256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidatePKCE 报告 verifier 在 method 下是否产生 challenge，使用
// ValidatePKCE 报告 verifier 在 method 下是否产生 challenge，使用 RFC 7636 第 4.2 节定义的
//
// code_challenge_method 值："S256" 时 challenge 必须等于 BASE64URL-ENCODE(SHA256(ASCII(verifier)))，即 S256 返回的值。"plain" 时
// 即 S256 返回的值。
// challenge 必须逐字等于 verifier。空 method 被视为 "plain"，这是 RFC 7636 在请求中缺少该参数时的默认值。
// 被视为 "plain"，这是 RFC 7636 在参数
// 从请求中缺失时的默认值。
//
// 任何其他 method 都未知并产生 false，即使字符串会
// 任何其他 method 都是未知的并产生 false，即使字符串在已知 method 下会匹配。verifier/challenge 的比较是
// 常量时间的。ValidatePKCE 是纯函数：不执行 I/O 且从不 panic，因此调用方可用它在把一对 PKCE 值发给 provider 之前诊断损坏的配对。
// 从不 panic，因此调用方可用它在把 PKCE 配对发给 provider 之前
// 诊断损坏的配对。
func ValidatePKCE(verifier, challenge, method string) bool {
	var want string
	switch method {
	case "", "plain":
		want = verifier
	case "S256":
		want = S256(verifier)
	default:
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(challenge)) == 1
}

// AuthorizeURL 构建授权码流程的浏览器 URL。
func AuthorizeURL(cfg Config, state, challenge string) (string, error) {
	if cfg.AuthorizeURL == "" {
		return "", errors.New("auth: config has no authorize endpoint")
	}
	if cfg.ClientID == "" {
		return "", errors.New("auth: config has no client id")
	}
	if challenge == "" {
		return "", errors.New("auth: missing PKCE challenge")
	}
	parsed, err := url.Parse(cfg.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("auth: bad authorize endpoint: %w", err)
	}
	query := parsed.Query()
	// 先应用 provider extras，使它们能提供可选
	// 参数（prompt、access_type 等），但协议关键值
	// 参数，但下面设置的协议关键值始终胜出。让 Extra 条目覆盖 `state` 会破坏
	// Callback.Wait 中的 CSRF 检查；覆盖 PKCE challenge 或 method
	// 会使 Exchange 发送的 verifier 在令牌端点失败。
	for key, value := range cfg.Extra {
		query.Set(key, value)
	}
	query.Set("response_type", "code")
	query.Set("client_id", cfg.ClientID)
	query.Set("state", state)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	if cfg.RedirectURI != "" {
		query.Set("redirect_uri", cfg.RedirectURI)
	}
	if len(cfg.Scopes) > 0 {
		query.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	if cfg.Audience != "" {
		query.Set("audience", cfg.Audience)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	Scope            string `json:"scope"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// tokenError 是结构化的 OAuth 错误响应。设备轮询检查其 code，因此端点状态随消息一起传递。
// code，因此端点状态随消息一起传递。
type tokenError struct {
	Status      int
	Code        string
	Description string
}

func (e *tokenError) Error() string {
	detail := e.Code
	if e.Description != "" {
		detail = e.Code + ": " + e.Description
	}
	if detail == "" {
		detail = http.StatusText(e.Status)
	}
	return fmt.Sprintf("auth: token endpoint returned HTTP %d (%s)", e.Status, detail)
}

func (c Config) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Exchange 用授权码换取令牌。
func Exchange(ctx context.Context, cfg Config, code, verifier string) (Token, error) {
	if code == "" {
		return Token{}, errors.New("auth: missing authorization code")
	}
	if verifier == "" {
		return Token{}, errors.New("auth: missing PKCE verifier")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", cfg.ClientID)
	form.Set("code_verifier", verifier)
	if cfg.RedirectURI != "" {
		form.Set("redirect_uri", cfg.RedirectURI)
	}
	response, err := postToken(ctx, cfg, form)
	if err != nil {
		return Token{}, err
	}
	return tokenFromResponse(cfg, response, Token{})
}

// Refresh 用刷新令牌换取新的访问令牌。先前的令牌
// Refresh 用刷新令牌换取新的访问令牌。先前的令牌提供 provider、种类、账号和刷新令牌回退。
func Refresh(ctx context.Context, cfg Config, token Token) (Token, error) {
	if token.Refresh == "" {
		return Token{}, errors.New("auth: token has no refresh token")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", token.Refresh)
	form.Set("client_id", cfg.ClientID)
	response, err := postToken(ctx, cfg, form)
	if err != nil {
		return Token{}, err
	}
	return tokenFromResponse(cfg, response, token)
}

func postToken(ctx context.Context, cfg Config, form url.Values) (tokenResponse, error) {
	var empty tokenResponse
	if cfg.TokenURL == "" {
		return empty, errors.New("auth: config has no token endpoint")
	}
	// 机密客户端在令牌端点用其
	// 机密客户端在令牌端点用其秘密认证（RFC 6749 第 2.3.1 节）。公开 PKCE 客户端留空，因此不为它们添加
	// 任何参数。在这里做此事一处覆盖 code 交换、刷新和设备授权。
	// 刷新和设备授权在一处覆盖。
	if cfg.ClientSecret != "" {
		form.Set("client_secret", cfg.ClientSecret)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return empty, fmt.Errorf("auth: build the token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := cfg.httpClient().Do(request)
	if err != nil {
		return empty, fmt.Errorf("auth: token endpoint: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, tokenBodyLimit))
	if err != nil {
		return empty, fmt.Errorf("auth: read the token response: %w", err)
	}
	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		if response.StatusCode/100 != 2 {
			return empty, &tokenError{Status: response.StatusCode}
		}
		return empty, fmt.Errorf("auth: token endpoint returned HTTP %d with a non-JSON body", response.StatusCode)
	}
	if response.StatusCode/100 != 2 || parsed.Error != "" {
		return parsed, &tokenError{
			Status:      response.StatusCode,
			Code:        parsed.Error,
			Description: parsed.ErrorDescription,
		}
	}
	return parsed, nil
}

// tokenFromResponse 把线上响应转换为存储的令牌。expires_in
// tokenFromResponse 把线上响应转换为存储的令牌。expires_in 被 expirySafety 缩短，使刷新发生在 provider 自己的截止时间之前。
// deadline.
func tokenFromResponse(cfg Config, response tokenResponse, previous Token) (Token, error) {
	if response.AccessToken == "" {
		return Token{}, errors.New("auth: token endpoint returned no access_token")
	}
	token := Token{
		Provider: cfg.Provider,
		Kind:     KindOAuth,
		Access:   response.AccessToken,
		Refresh:  response.RefreshToken,
		Account:  previous.Account,
	}
	if token.Provider == "" {
		token.Provider = previous.Provider
	}
	if token.Refresh == "" {
		token.Refresh = previous.Refresh
	}
	if response.ExpiresIn > 0 {
		lifetime := time.Duration(response.ExpiresIn) * time.Second
		// 安全余量是提前刷新的提示，而非丢弃
		// 安全余量是提前刷新的提示，而非丢弃仍有效凭据的理由。当 provider 签发的令牌整个生命周期都落在余量内时，把余量
		// 令牌整个生命周期都落在余量内时，把余量
		// 限制为生命周期的一半，使令牌在接近其真实截止时间前
		// 限制为生命周期的一半，使令牌在接近其真实截止时间前仍可用，而不是在签发时就被报告为已过期。
		margin := expirySafety
		if lifetime <= margin {
			margin = lifetime / 2
		}
		token.ExpiresAt = time.Now().Add(lifetime - margin).Truncate(time.Second)
	}
	return token, nil
}

// ErrStateMismatch 在 provider 回显意外 state 时返回，这意味着回调不属于本次登录尝试。
// 这意味着回调不属于本次登录尝试。
var ErrStateMismatch = errors.New("auth: oauth state mismatch")

// ErrNoAuthorizationCode 在重定向携带预期
// ErrNoAuthorizationCode 在重定向携带预期 state 但既无授权码也无错误时返回。这样的回调无法完成登录，绝不能被报告为成功。
// 完成登录，绝不能被报告为成功。
var ErrNoAuthorizationCode = errors.New("auth: oauth callback has no authorization code")

// ErrCallbackClosed 在重定向到达前调用 Callback.Close 时由 Wait 返回。只关闭监听器无法解除 Wait 阻塞，因为 HTTP 服务器只是停止服务而永不投递结果。
// 重定向到达。只关闭监听器无法解除 Wait 阻塞，因为
// HTTP 服务器只是停止服务且永不投递结果。
var ErrCallbackClosed = errors.New("auth: oauth callback closed")

// Callback 是接收一个 OAuth 重定向的 loopback 监听器。
type Callback struct {
	listener net.Listener
	// closed 由 Close 发信号，使待处理的 Wait 能解除阻塞，即使调用方没有提供可取消的 context。
	// 调用方没有提供可取消的 context。
	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error
}

// Listen 绑定 addr（空表示 127.0.0.1:0）并返回待处理的回调。
func Listen(addr string) (*Callback, error) {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:0"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("auth: bind the OAuth callback: %w", err)
	}
	return &Callback{listener: listener, closed: make(chan struct{})}, nil
}

// Addr 是绑定的 loopback 地址，例如 127.0.0.1:51234。
func (c *Callback) Addr() string { return c.listener.Addr().String() }

// URL 是要交给 provider 的重定向 URI。
func (c *Callback) URL() string { return "http://" + c.Addr() + "/callback" }

// Wait 服务重定向并返回授权码。每个响应
// Wait 服务重定向并返回授权码。每个响应都是一个小的 HTML 页面，使浏览器显示友好内容。当 state 非空时必须匹配回调的 state 参数。
// 非空时必须匹配回调的 state 参数。
func (c *Callback) Wait(ctx context.Context, state string) (string, error) {
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	deliver := func(value result) { once.Do(func() { done <- value }) }

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if query.Get("code") == "" && query.Get("error") == "" && query.Get("state") == "" {
			writePage(writer, http.StatusNotFound, "Not found",
				"Waiting for the OAuth callback on this port.")
			return
		}
		if state != "" && query.Get("state") != state {
			writePage(writer, http.StatusBadRequest, "Authorization failed",
				"The state parameter does not match this login attempt.")
			deliver(result{err: ErrStateMismatch})
			return
		}
		if failure := query.Get("error"); failure != "" {
			message := query.Get("error_description")
			if message == "" {
				message = failure
			}
			writePage(writer, http.StatusBadRequest, "Authorization failed", message)
			deliver(result{err: fmt.Errorf("auth: authorization failed: %s", message)})
			return
		}
		code := query.Get("code")
		if code == "" {
			// 携带正确 state 但没有 code（也没有错误）的重定向
			// 不是一次完成的登录。把它当作失败，使调用方永不
			// 携带正确 state 但没有 code（也没有错误）的重定向不是一次完成的登录。把它当作失败，使调用方永不收到空 code 加 nil 错误，且浏览器
			// 不会在授权实际未成功时被告知成功。
			writePage(writer, http.StatusBadRequest, "Authorization failed",
				"The authorization server did not return an authorization code.")
			deliver(result{err: ErrNoAuthorizationCode})
			return
		}
		writePage(writer, http.StatusOK, "Authorization complete",
			"You can close this window and return to argentgo.")
		deliver(result{code: code})
	})

	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(c.listener) }()
	defer server.Close()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.closed:
		return "", ErrCallbackClosed
	case value := <-done:
		return value.code, value.err
	}
}

// Close 释放监听器并取消待处理的 Wait。可安全多次调用；第一次调用的监听器错误返回给调用方。
// 可安全多次调用；第一次调用的监听器错误返回给调用方。
func (c *Callback) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.listener.Close()
		close(c.closed)
	})
	return c.closeErr
}

// WaitCallback 绑定 addr、等待重定向并返回 code。它不检查 state 参数；当预期 state 已知时使用 WaitCallbackState（通常情况）。
// 它不检查 state 参数；当预期 state 已知时使用
// WaitCallbackState（通常情况）。
func WaitCallback(ctx context.Context, addr string) (string, error) {
	return WaitCallbackState(ctx, addr, "")
}

// WaitCallbackState 绑定 addr，仅在重定向携带预期 state 时返回 code。
// 携带预期 state。
func WaitCallbackState(ctx context.Context, addr, state string) (string, error) {
	callback, err := Listen(addr)
	if err != nil {
		return "", err
	}
	defer callback.Close()
	return callback.Wait(ctx, state)
}

func writePage(writer http.ResponseWriter, status int, title, message string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	fmt.Fprintf(writer, `<!doctype html><meta charset="utf-8"><title>argentgo</title>`+
		`<body style="font-family:system-ui;padding:3rem"><h1>%s</h1><p>%s</p>`,
		html.EscapeString(title), html.EscapeString(message))
}
