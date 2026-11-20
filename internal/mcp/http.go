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
	// maxHTTPBody caps a JSON response body read from an HTTP server.
	maxHTTPBody = 8 * 1024 * 1024
	// handshakeWindow bounds how long Connect waits for an SSE endpoint event.
	handshakeWindow = 2 * time.Second
)

// httpTransport speaks JSON-RPC over HTTP.
//
// Two shapes are supported. A legacy SSE server answers the GET with an event
// stream whose first "endpoint" event names the URL requests are POSTed to
// (usually /message?sessionId=...). A streamable server takes POSTs at the
// URL itself and answers with JSON (or its own event stream), and refuses the
// GET; that refusal selects the streamable shape.
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

	// The event stream must outlive Connect, so it uses its own cancellable
	// context with no deadline. The *handshake* (waiting for the GET response
	// headers) is bounded by ctx instead: otherwise a server that accepts the
	// GET but never answers would make Connect hang forever, ignoring both the
	// caller's deadline and the configured timeout.
	streamCtx, cancel := context.WithCancel(context.Background())
	t.streamCancel = cancel
	if err := t.openStream(ctx, streamCtx, cancel); err != nil {
		// No event stream: treat the URL as a streamable single endpoint.
		cancel()
		return t, nil
	}

	// A legacy SSE server announces its POST endpoint on the stream. readStream
	// applies every valid announcement directly to postURL, including one that
	// arrives after this window closes. Waiting here only lets a prompt
	// announcement land before Connect returns so the first request already
	// uses it; a late one is still honoured by readStream.
	t.awaitEndpoint(ctx, timeout)
	return t, nil
}

// setEndpoint retargets subsequent POSTs to a stream-announced endpoint. An
// empty value (a cross-origin or malformed announcement rejected by
// resolveEndpoint) is ignored so configured headers never leave the origin.
// It is safe to call at any time, including after the handshake window.
func (t *httpTransport) setEndpoint(resolved string) {
	if resolved == "" {
		return
	}
	t.mu.Lock()
	t.postURL = resolved
	t.mu.Unlock()
}

// openStream starts the long-lived GET event stream.
//
// The request uses streamCtx, which has no deadline so the stream can live for
// the whole session. But only the *handshake* — receiving the response headers
// — is allowed to take ctx's remaining time. ctx carries the caller's deadline
// or the configured request timeout (set by newHTTPTransport). Waiting on the
// request context alone would let a server that accepts the GET and then goes
// silent block Connect indefinitely, leaking the connection and the goroutine.
//
// streamCancel aborts the in-flight GET when ctx ends before the headers do.
// The caller then treats the URL as a streamable single endpoint, so no stream
// is lost by the abort.
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
		// Abort the in-flight GET. If it won the race and produced a response,
		// close the body once the Do goroutine hands it over so nothing leaks.
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

// readStream feeds SSE messages into the waiters and records the endpoint.
func (t *httpTransport) readStream(ctx context.Context, body io.ReadCloser) {
	defer body.Close()
	err := forEachSSE(body, func(event, data string) {
		if event == "endpoint" {
			resolved := resolveEndpoint(t.url, strings.TrimSpace(data))
			t.setEndpoint(resolved)
			// Keep feeding the handshake window too; a prompt announcement
			// still lets Connect return as soon as it is seen.
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

// awaitEndpoint waits for the endpoint announcement, but not forever: a
// streamable server may accept a GET stream yet never send one.
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
		// A streamable server may keep the POST's event stream open so it can
		// push server-initiated messages later. Draining it inline would block
		// this call until the stream closes (or the request deadline expires),
		// even though the reply for our id may already have been delivered by
		// the first event. Process the stream in the background and let the
		// waiter below decide when this call is done; the deferred Close then
		// releases the stream. Responses for other in-flight calls are still
		// routed through the shared waiters, so concurrent calls keep working.
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

// post sends one frame to the request endpoint.
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

// fail closes the transport once, releasing every waiter.
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

// forEachSSE decodes server-sent events from r.
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

// resolveEndpoint resolves the announced POST URL against the stream URL, but
// only accepts an endpoint on the same origin (scheme and host) as the stream.
// A server announcement pointing elsewhere is rejected with an empty string so
// the caller falls back to the original URL: configured headers may carry
// credentials, and they must never be forwarded to a different origin.
func resolveEndpoint(streamURL, announced string) string {
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
