package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// maxHTTPBody 限制从 HTTP 服务器读取的 JSON 响应体大小。
	maxHTTPBody = 8 * 1024 * 1024
	// handshakeWindow 限制 Connect 等待 SSE 端点事件的时间。
	handshakeWindow = 2 * time.Second
)

// httpTransport 通过 HTTP 传输 JSON-RPC。
//
// 支持两种形态。旧的 SSE 服务器用事件流回复 GET，其第一个 "endpoint"
// 事件指明请求 POST 到哪个 URL（通常是 /message?sessionId=...）。
// 流式服务器直接在 URL 上接受 POST，并用 JSON（或其自己的事件流）
// 回复，同时拒绝 GET；
// 该拒绝即选择流式形态。
type httpTransport struct {
	name    string
	url     string
	headers map[string]string
	client  *http.Client

	ids     idSequence
	waiters *waiters

	endpointCh chan string

	mu        sync.Mutex
	postURL   string
	sessionID string
	err       error

	streamCancel context.CancelFunc
	done         chan struct{}
	failOnce     sync.Once
	closeOnce    sync.Once
}

func newHTTPTransport(ctx context.Context, spec Spec) (*httpTransport, error) {
	if _, err := url.Parse(spec.URL); err != nil {
		return nil, fmt.Errorf("mcp %q: invalid URL %q: %w", spec.Name, spec.URL, err)
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	t := &httpTransport{
		name:       spec.Name,
		url:        spec.URL,
		headers:    spec.Headers,
		client:     &http.Client{},
		waiters:    newWaiters(),
		endpointCh: make(chan string, 1),
		postURL:    spec.URL,
		done:       make(chan struct{}),
	}

	// 事件流必须比 Connect 活得更久，因此它使用自己的、无截止时间的
	// 可取消 context。而*握手*（等待 GET 响应头）
	// 则受 ctx 限制：否则一个接受 GET 却从不回复的服务器
	// 会让 Connect 永远挂起，
	// 无视调用方的截止时间和配置的超时。
	streamCtx, cancel := context.WithCancel(context.Background())
	t.streamCancel = cancel
	if err := t.openStream(ctx, streamCtx, cancel); err != nil {
		// 没有事件流：把该 URL 当作流式单端点。
		cancel()
		return t, nil
	}

	// 旧的 SSE 服务器在流上宣告其 POST 端点。readStream
	// 把每个有效宣告直接应用到 postURL，包括在该窗口关闭后
	// 才到达的宣告。这里等待只是让较早的宣告在 Connect 返回前
	// 落地，使第一个请求就能使用它；
	// 较晚的宣告仍会被 readStream 采纳。
	t.awaitEndpoint(ctx, timeout)
	return t, nil
}

// setEndpoint 把后续 POST 重定向到流宣告的端点。
// 空值（被 resolveEndpoint 拒绝的跨域或格式错误宣告）会被忽略，
// 使配置的头部永远不会离开本源。
// 可以在任何时刻调用，包括握手窗口之后。
func (t *httpTransport) setEndpoint(resolved string) {
	if resolved == "" {
		return
	}
	t.mu.Lock()
	t.postURL = resolved
	t.mu.Unlock()
}

// openStream 启动长生命周期的 GET 事件流。
//
// 该请求使用 streamCtx，它没有截止时间，使流能持续
// 整个会话。但只有*握手*——接收响应头——
// 才允许消耗 ctx 的剩余时间。ctx 携带调用方的截止时间
// 或配置的请求超时（由 newHTTPTransport 设置）。只等待
// 请求上下文会让接受 GET 后沉默的服务器无限期阻塞 Connect，
// 泄漏连接和 goroutine。
//
// 如果 ctx 先于响应头结束，streamCancel 会中止在途的 GET。
// 调用方随后把该 URL 当作流式单端点，因此中止不会丢失任何流。
// 因此中止不会丢失任何流。
func (t *httpTransport) openStream(ctx, streamCtx context.Context, streamCancel context.CancelFunc) error {
	req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, t.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	applyHeaders(req, t.headers)

	type outcome struct {
		response *http.Response
		err      error
	}
	results := make(chan outcome, 1)
	go func() {
		response, err := t.client.Do(req)
		results <- outcome{response: response, err: err}
	}()

	select {
	case result := <-results:
		if result.err != nil {
			return result.err
		}
		response := result.response
		if response.StatusCode < 200 || response.StatusCode > 299 {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			return fmt.Errorf("mcp %q: GET %s returned %d", t.name, t.url, response.StatusCode)
		}
		if contentType := strings.ToLower(response.Header.Get("Content-Type")); !strings.Contains(contentType, "text/event-stream") {
			_ = response.Body.Close()
			return fmt.Errorf("mcp %q: %s is not an event stream", t.name, t.url)
		}
		go t.readStream(streamCtx, response.Body)
		return nil
	case <-ctx.Done():
		// 中止在途的 GET。如果它赢得竞争并产生了响应，
		// 在 Do goroutine 交出响应体后关闭它，确保没有泄漏。
		streamCancel()
		go func() {
			result := <-results
			if result.response != nil {
				_ = result.response.Body.Close()
			}
		}()
		return fmt.Errorf("mcp %q: GET %s during handshake: %w", t.name, t.url, ctx.Err())
	}
}

// readStream 把 SSE 消息喂给等待者并记录端点。
func (t *httpTransport) readStream(ctx context.Context, body io.ReadCloser) {
	defer body.Close()
	err := forEachSSE(body, func(event, data string) {
		if event == "endpoint" {
			announced := strings.TrimSpace(data)
			if announced == "" {
				// 空宣告不得重定向 POST。没有这个保护，
				// resolveEndpoint 会把空引用解析为流 URL 本身，
				// 覆盖之前宣告的 POST 端点，
				// 并在无声中破坏连接。
				return
			}
			resolved := resolveEndpoint(t.url, announced)
			t.setEndpoint(resolved)
			// 也继续供给握手窗口；较早的宣告
			// 仍能让 Connect 在看到它时立即返回。
			select {
			case t.endpointCh <- resolved:
			default:
			}
			return
		}
		_ = t.waiters.handle([]byte(data))
	})
	if err != nil && ctx.Err() == nil {
		t.fail(fmt.Errorf("mcp %q: event stream failed: %w", t.name, err))
	}
}

// awaitEndpoint 等待端点宣告，但不是永远：流式
// 服务器可能接受 GET 流却从不发送端点宣告。
func (t *httpTransport) awaitEndpoint(ctx context.Context, timeout time.Duration) (string, bool) {
	window := handshakeWindow
	if timeout > 0 && timeout < window {
		window = timeout
	}
	timer := time.NewTimer(window)
	defer timer.Stop()
	select {
	case endpoint := <-t.endpointCh:
		return endpoint, true
	case <-timer.C:
		return "", false
	case <-ctx.Done():
		return "", false
	}
}

func (t *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := t.ids.take()
	ch := t.waiters.add(id)
	defer t.waiters.drop(id)

	frame, err := marshalRequest(id, method, params)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: encode %s: %w", t.name, method, err)
	}
	response, err := t.post(ctx, frame)
	if err != nil {
		return nil, fmt.Errorf("mcp %q: %s: %w", t.name, method, err)
	}
	defer response.Body.Close()

	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") {
		// 流式服务器可能保持 POST 的事件流打开，
		// 以便稍后推送服务器发起的消息。
		// 内联排空它会阻塞本次调用直到流关闭（或请求截止时间到期），
		// 即使我们 id 的回复可能已经由第一个事件送达。
		// 在后台处理该流，让下面的等待者决定调用何时结束；
		// 延迟的 Close 随后释放它。其他在途调用的回复仍通过
		// 共享的等待者路由，
		// 因此并发调用继续工作。
		go func() {
			_ = forEachSSE(response.Body, func(event, data string) {
				if event == "endpoint" {
					return
				}
				_ = t.waiters.handle([]byte(data))
			})
		}()
	} else {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxHTTPBody))
		if readErr != nil && len(bytes.TrimSpace(body)) == 0 {
			return nil, fmt.Errorf("mcp %q: %s: read response: %w", t.name, method, readErr)
		}
		if len(bytes.TrimSpace(body)) > 0 {
			if err := t.waiters.handle(body); err != nil {
				return nil, fmt.Errorf("mcp %q: %s: %w", t.name, method, err)
			}
		}
	}

	select {
	case reply := <-ch:
		return reply.result, reply.err
	case <-ctx.Done():
		return nil, fmt.Errorf("mcp %q: %s: %w", t.name, method, ctx.Err())
	case <-t.done:
		return nil, t.closedError(method)
	}
}

func (t *httpTransport) notify(ctx context.Context, method string, params any) error {
	frame, err := marshalNotification(method, params)
	if err != nil {
		return fmt.Errorf("mcp %q: encode %s: %w", t.name, method, err)
	}
	response, err := t.post(ctx, frame)
	if err != nil {
		return fmt.Errorf("mcp %q: %s: %w", t.name, method, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	return nil
}

// post 发送一帧到请求端点。
func (t *httpTransport) post(ctx context.Context, frame []byte) (*http.Response, error) {
	t.mu.Lock()
	target := t.postURL
	session := t.sessionID
	t.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	applyHeaders(req, t.headers)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}

	response, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if value := response.Header.Get("Mcp-Session-Id"); value != "" {
		t.mu.Lock()
		t.sessionID = value
		t.mu.Unlock()
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return response, nil
}

func (t *httpTransport) Close() error {
	t.closeOnce.Do(func() {
		if t.streamCancel != nil {
			t.streamCancel()
		}
		t.fail(fmt.Errorf("mcp %q: client closed", t.name))
		t.client.CloseIdleConnections()
	})
	return nil
}

// fail 只关闭一次传输，释放所有等待者。
func (t *httpTransport) fail(err error) {
	t.failOnce.Do(func() {
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
		close(t.done)
		t.waiters.failAll(err)
	})
}

func (t *httpTransport) closedError(method string) error {
	t.mu.Lock()
	err := t.err
	t.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("mcp %q: connection closed", t.name)
	}
	if method == "" {
		return err
	}
	return fmt.Errorf("%w (during %s)", err, method)
}

// forEachSSE 从 r 解码服务器发送的事件。
func forEachSSE(r io.Reader, handle func(event, data string)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)

	var event string
	var data []string
	flush := func() {
		if len(data) == 0 {
			event = ""
			return
		}
		name := event
		if name == "" {
			name = "message"
		}
		handle(name, strings.Join(data, "\n"))
		event = ""
		data = data[:0]
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if found {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "event":
			event = value
		case "data":
			data = append(data, value)
		}
	}
	flush()
	return scanner.Err()
}

// resolveEndpoint 把宣告的 POST URL 相对于流 URL 解析，但
// 只接受与流同源（scheme 和 host）的端点。
// 指向别处的服务器宣告会被拒绝（返回空字符串），使
// 调用方回退到原始 URL：配置的头部可能携带
// 凭据，绝不能转发到不同的源。
func resolveEndpoint(streamURL, announced string) string {
	if strings.TrimSpace(announced) == "" {
		// 空引用会解析为基础 URL，那会把 POST 重定向到 SSE 端点。
		// 应把它当作没有宣告，使调用方
		// 能回退到当前的 POST URL。
		return ""
	}
	base, err := url.Parse(streamURL)
	if err != nil {
		return ""
	}
	parsed, err := url.Parse(announced)
	if err != nil {
		return ""
	}
	ref := parsed
	if !parsed.IsAbs() {
		ref = base.ResolveReference(parsed)
	}
	if !strings.EqualFold(ref.Scheme, base.Scheme) || !strings.EqualFold(ref.Host, base.Host) {
		return ""
	}
	return ref.String()
}

func applyHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		req.Header.Set(key, value)
	}
}
