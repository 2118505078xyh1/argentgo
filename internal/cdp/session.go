package cdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Session 是一条 CDP 会话：把 WebSocket 上的 JSON 消息按 id 匹配到请求，
// 并把事件投递给订阅者。
type Session struct {
	ws     *Conn
	nextID atomic.Int64

	mu      sync.Mutex
	pending map[int64]chan rpcResponse
	events  map[string][]func(json.RawMessage)
	closed  bool
	done    chan struct{}
}

type rpcMessage struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type rpcResponse struct {
	result json.RawMessage
	err    error
}

// Dial 建立到 wsURL 的 CDP 会话并启动读取循环。
func Dial(ctx context.Context, wsURL string) (*Session, error) {
	ws, err := DialWS(ctx, wsURL)
	if err != nil {
		return nil, err
	}
	s := &Session{
		ws:      ws,
		pending: make(map[int64]chan rpcResponse),
		events:  make(map[string][]func(json.RawMessage)),
		done:    make(chan struct{}),
	}
	go s.readLoop()
	return s, nil
}

func (s *Session) readLoop() {
	defer close(s.done)
	for {
		payload, err := s.ws.ReadText()
		if err != nil {
			s.finish(err)
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}
		if msg.Method != "" {
			s.mu.Lock()
			handlers := make([]func(json.RawMessage), len(s.events[msg.Method]))
			copy(handlers, s.events[msg.Method])
			s.mu.Unlock()
			for _, h := range handlers {
				h(msg.Params)
			}
			continue
		}
		if msg.ID != 0 {
			s.mu.Lock()
			ch := s.pending[msg.ID]
			delete(s.pending, msg.ID)
			s.mu.Unlock()
			if ch == nil {
				continue
			}
			resp := rpcResponse{result: msg.Result}
			if msg.Error != nil {
				resp.err = fmt.Errorf("cdp: %s (code %d)", msg.Error.Message, msg.Error.Code)
			}
			ch <- resp
		}
	}
}

func (s *Session) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	for id, ch := range s.pending {
		ch <- rpcResponse{err: err}
		delete(s.pending, id)
	}
}

// Call 发送一条 CDP 命令并等待响应。
func (s *Session) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	ch := make(chan rpcResponse, 1)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("cdp: session closed")
	}
	s.pending[id] = ch
	s.mu.Unlock()

	msg := rpcMessage{ID: id, Method: method}
	if params != nil {
		b, _ := json.Marshal(params)
		msg.Params = b
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		s.forget(id)
		return nil, err
	}
	if err := s.ws.WriteText(raw); err != nil {
		s.forget(id)
		return nil, err
	}
	select {
	case resp := <-ch:
		return resp.result, resp.err
	case <-ctx.Done():
		s.forget(id)
		return nil, ctx.Err()
	case <-s.done:
		return nil, errors.New("cdp: connection closed")
	}
}

func (s *Session) forget(id int64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

// Close 关闭会话。
func (s *Session) Close() error {
	s.finish(errors.New("cdp: closed"))
	return s.ws.Close()
}
