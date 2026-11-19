package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// jsonrpcVersion is the only protocol version this package speaks.
const jsonrpcVersion = "2.0"

// rpcRequest is one JSON-RPC 2.0 call that expects an answer.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcNotification is one JSON-RPC 2.0 message that expects no answer.
type rpcNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// rpcErrorBody is the "error" member of a failed response.
type rpcErrorBody struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcErrorBody) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("json-rpc error %d", e.Code)
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

// rpcReply is whichever half of a response arrived: a result or an error.
type rpcReply struct {
	result json.RawMessage
	err    error
}

// idSequence hands out monotonically increasing request ids.
type idSequence struct {
	mu   sync.Mutex
	next uint64
}

func (s *idSequence) take() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return s.next
}

// waiters correlates responses with the calls that are waiting for them.
type waiters struct {
	mu      sync.Mutex
	pending map[uint64]chan rpcReply
}

func newWaiters() *waiters {
	return &waiters{pending: make(map[uint64]chan rpcReply)}
}

func (w *waiters) add(id uint64) chan rpcReply {
	ch := make(chan rpcReply, 1)
	w.mu.Lock()
	w.pending[id] = ch
	w.mu.Unlock()
	return ch
}

func (w *waiters) drop(id uint64) {
	w.mu.Lock()
	delete(w.pending, id)
	w.mu.Unlock()
}

func (w *waiters) deliver(id uint64, reply rpcReply) bool {
	w.mu.Lock()
	ch, ok := w.pending[id]
	w.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- reply:
	default:
		// A late duplicate must not block the reader goroutine.
	}
	return true
}

func (w *waiters) failAll(err error) {
	w.mu.Lock()
	pending := w.pending
	w.pending = make(map[uint64]chan rpcReply)
	w.mu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- rpcReply{err: err}:
		default:
		}
	}
}

// handle decodes one payload (object or batch) and delivers its responses.
// Server-initiated notifications and requests are ignored: this client does
// not advertise sampling or roots handlers.
func (w *waiters) handle(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			return fmt.Errorf("mcp: invalid JSON-RPC batch: %w", err)
		}
		for _, item := range batch {
			if err := w.handleOne(item); err != nil {
				return err
			}
		}
		return nil
	}
	return w.handleOne(trimmed)
}

func (w *waiters) handleOne(raw []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("mcp: invalid JSON-RPC message: %w", err)
	}
	if _, hasMethod := object["method"]; hasMethod {
		return nil
	}
	idRaw, hasID := object["id"]
	if !hasID || string(idRaw) == "null" {
		return nil
	}
	id, ok := decodeID(idRaw)
	if !ok {
		return nil
	}
	if errRaw, hasError := object["error"]; hasError && string(errRaw) != "null" {
		var body rpcErrorBody
		if err := json.Unmarshal(errRaw, &body); err != nil {
			return fmt.Errorf("mcp: invalid JSON-RPC error: %w", err)
		}
		w.deliver(id, rpcReply{err: &body})
		return nil
	}
	result := json.RawMessage("null")
	if res, hasResult := object["result"]; hasResult {
		result = append(json.RawMessage(nil), res...)
	}
	w.deliver(id, rpcReply{result: result})
	return nil
}

// decodeID accepts the numeric and string id shapes JSON-RPC allows.
func decodeID(raw json.RawMessage) (uint64, bool) {
	var number uint64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if value, err := strconv.ParseUint(text, 10, 64); err == nil {
			return value, true
		}
	}
	return 0, false
}

// marshalRequest builds a request frame.
func marshalRequest(id uint64, method string, params any) ([]byte, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rpcRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: raw})
}

// marshalNotification builds a notification frame.
func marshalNotification(method string, params any) ([]byte, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rpcNotification{JSONRPC: jsonrpcVersion, Method: method, Params: raw})
}

func marshalParams(params any) (json.RawMessage, error) {
	switch value := params.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		return value, nil
	default:
		return json.Marshal(value)
	}
}

// methodNotFound reports a JSON-RPC "method not found" error.
func methodNotFound(err error) bool {
	var body *rpcErrorBody
	if !errors.As(err, &body) {
		return false
	}
	return body.Code == -32601
}
