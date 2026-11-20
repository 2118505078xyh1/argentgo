package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sync"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// maxRPCFrame is the largest accepted frame on the line-delimited --rpc / acp
// transports. It keeps the old bufio.Scanner 4 MiB ceiling as an explicit,
// enforced limit. A longer frame is rejected with a structured JSON-RPC error
// and dropped; the transport keeps reading the next frame.
const maxRPCFrame = 4 * 1024 * 1024

// errFrameTooLong marks one dropped oversized frame. It is deliberately not a
// terminal transport error: scanLines has already consumed the whole frame and
// resumes normal reading afterwards.
var errFrameTooLong = fmt.Errorf("json-rpc frame too long (max %d bytes)", maxRPCFrame)

// rpcAbsPath matches an absolute filesystem path embedded in a session-layer
// error. Both Windows drive roots (C:\...) and POSIX roots (/...) are covered;
// a leading delimiter is kept so the surrounding wording stays readable. The
// trailing class stops at whitespace and the usual quoting/colon delimiters so
// the error reason that follows a path is preserved.
var rpcAbsPath = regexp.MustCompile(`(^|[\s"'=(:;])((?:[A-Za-z]:[\\/])|/)[^\s"'<>|:]*`)

// scrubSessionLoadError prepares a session.load failure for the JSON-RPC reply.
// Session-layer errors interpolate the absolute storage directory (e.g.
// C:\Users\<user>\.argentgo\sessions\<id>.jsonl), which must not cross the RPC
// boundary. Credential-shaped spans are redacted and absolute paths are folded
// to a placeholder; the canonical reason (and the caller-supplied session id)
// survive so the client still learns why the load failed. The unredacted detail
// stays on the server side for logging.
func scrubSessionLoadError(id string, err error) string {
	msg := security.Redact(err.Error())
	msg = rpcAbsPath.ReplaceAllString(msg, "${1}<storage>/...")
	return fmt.Sprintf("load session %q: %s", id, msg)
}

// scanResult is one item from scanLines: a trimmed, non-empty input line, or
// an error. errFrameTooLong means a single oversized frame was dropped and
// reading continues; any other error is terminal (a real I/O failure on in).
type scanResult struct {
	line []byte
	err  error
}

// scanLines reads newline-delimited input on a background goroutine so a
// transport can react to ctx cancellation even while stdin has no data ready —
// a bare blocking Read would block the transport's own goroutine and swallow
// Ctrl-C.
//
// It uses bufio.Reader rather than bufio.Scanner because a Scanner cannot
// recover from bufio.ErrTooLong: once a line overflows its buffer it stops for
// good, which used to tear down the entire RPC/ACP connection on one bad frame.
// Here an oversized line is drained (without buffering it) and reported through
// errFrameTooLong, after which normal scanning resumes. Accumulated bytes are
// copied because ReadSlice's slice is only valid until the next read.
func scanLines(ctx context.Context, in io.Reader) <-chan scanResult {
	results := make(chan scanResult)
	go func() {
		defer close(results)
		br := bufio.NewReaderSize(in, 64*1024)
		line := make([]byte, 0, 64*1024)
		tooLong := false
		send := func(res scanResult) bool {
			select {
			case results <- res:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for {
			chunk, err := br.ReadSlice('\n')
			if err != bufio.ErrBufferFull && len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
				chunk = chunk[:len(chunk)-1] // exclude the delimiter from the limit
			}
			if !tooLong {
				if len(line)+len(chunk) > maxRPCFrame {
					tooLong = true
					line = line[:0] // stop buffering the rest of this frame
				} else {
					line = append(line, chunk...)
				}
			}
			if err == bufio.ErrBufferFull {
				continue // the frame continues in the next chunk
			}
			if err != nil && err != io.EOF {
				send(scanResult{err: err}) // terminal I/O error
				return
			}
			if tooLong {
				if !send(scanResult{err: errFrameTooLong}) {
					return
				}
			} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				if !send(scanResult{line: append([]byte(nil), trimmed...)}) {
					return
				}
			}
			line = line[:0]
			tooLong = false
			if err == io.EOF {
				return
			}
		}
	}()
	return results
}

// reportScanError turns a recoverable scan error into a structured JSON-RPC
// reply. It reports whether the error is terminal and the transport must stop.
// An oversized frame has no parseable id, so it is answered with id: null, the
// same shape used for ordinary parse errors.
func reportScanError(p *peer, err error) (terminal bool) {
	if !errors.Is(err, errFrameTooLong) {
		return true
	}
	p.fail(json.RawMessage("null"), codeParseError, "parse error: "+err.Error())
	return false
}

// RunRPC serves newline-delimited JSON-RPC 2.0 over in/out until in reaches
// EOF or ctx is cancelled.
//
// Methods: prompt, abort, session.list, session.load, approval.list,
// event/subscribe. Streamed
// prompt text is emitted as prompt/delta notifications before the prompt result
// is returned; event/subscribe answers with the current seq then emits "event"
// notifications until the transport ends.
func RunRPC(ctx context.Context, in io.Reader, out io.Writer, cfg Config) error {
	ctx, cancel := context.WithCancel(ctx)

	hub := selectHub(cfg)

	p := newPeer(out)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()

	results := scanLines(ctx, in)
	for {
		// Prioritise cancellation so a queued line cannot delay the exit.
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		select {
		case <-ctx.Done():
			return nil
		case res, ok := <-results:
			if !ok { // EOF
				return nil
			}
			if res.err != nil {
				if reportScanError(p, res.err) {
					return res.err
				}
				continue
			}
			var req request
			if err := json.Unmarshal(res.line, &req); err != nil {
				p.fail(json.RawMessage("null"), codeParseError, "parse error: "+err.Error())
				continue
			}
			if req.Method == "" {
				p.fail(req.ID, codeInvalidRequest, "missing method")
				continue
			}
			handleRPCRequest(ctx, p, cfg, req, &wg, hub)
		}
	}
}

func handleRPCRequest(ctx context.Context, p *peer, cfg Config, req request, wg *sync.WaitGroup, hub *EventHub) {
	switch req.Method {
	case "prompt":
		text := promptText(req.Params)
		if text == "" {
			p.fail(req.ID, codeInvalidParams, "prompt has no text")
			return
		}
		if cfg.Runner == nil {
			p.fail(req.ID, codeServerError, "no runner configured")
			return
		}
		id := req.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := cfg.Runner.Prompt(ctx, text, func(chunk string) {
				if chunk == "" {
					return
				}
				p.notify("prompt/delta", map[string]any{"text": chunk})
			})
			if err != nil {
				if isCancelled(ctx, err) {
					// A user abort is a normal stop, not an internal error. Keep
					// a result-shaped reply (with the partial answer) so clients
					// can tell it apart from a real -32603 failure.
					p.respond(id, map[string]any{"text": result, "stopReason": "cancelled"})
					return
				}
				p.fail(id, codeInternalError, err.Error())
				return
			}
			p.respond(id, map[string]any{"text": result})
		}()
	case "abort":
		if cfg.Runner == nil {
			p.fail(req.ID, codeServerError, "no runner configured")
			return
		}
		cfg.Runner.Abort()
		p.respond(req.ID, map[string]any{"ok": true})
	case "session.list":
		if cfg.Sessions == nil {
			p.fail(req.ID, codeServerError, "session store not configured")
			return
		}
		infos, err := cfg.Sessions.List()
		if err != nil {
			p.fail(req.ID, codeInternalError, err.Error())
			return
		}
		if infos == nil {
			infos = []SessionInfo{}
		}
		p.respond(req.ID, map[string]any{"sessions": infos})
	case "session.load":
		if cfg.Sessions == nil {
			p.fail(req.ID, codeServerError, "session store not configured")
			return
		}
		var params struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionId"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		id := params.ID
		if id == "" {
			id = params.SessionID
		}
		if id == "" {
			p.fail(req.ID, codeInvalidParams, "session id is required")
			return
		}
		events, err := cfg.Sessions.Load(id)
		if err != nil {
			p.fail(req.ID, codeServerError, scrubSessionLoadError(id, err))
			return
		}
		if events == nil {
			events = []SessionEvent{}
		}
		p.respond(req.ID, map[string]any{"id": id, "events": events})
	case "approval.list":
		// approval.list {session?} returns the runner's durable pending-approval
		// mirror so a reconnecting client can recover questions asked while it
		// was away. The store is discovered through a small optional interface,
		// mirroring EventHub(): fakes and standalone callers without one get a
		// clean "not configured" error instead of a panic.
		var params struct {
			Session string `json:"session"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		typed, ok := cfg.Runner.(interface{ PendingStore() *security.PendingStore })
		if !ok || typed.PendingStore() == nil {
			p.fail(req.ID, codeServerError, "pending approval store not configured")
			return
		}
		items, err := typed.PendingStore().List(params.Session)
		if err != nil {
			p.fail(req.ID, codeInternalError, err.Error())
			return
		}
		if items == nil {
			items = []security.PendingApproval{}
		}
		p.respond(req.ID, map[string]any{"pending": items})
	case "event/subscribe":
		// Streaming in a goroutine keeps the scanner reading further requests.
		// The method result reports the current seq; each event then arrives as
		// an "event" notification. Cancelling the transport (EOF or ctx) stops it.
		var params struct {
			After uint64 `json:"after"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				p.fail(req.ID, codeInvalidParams, "invalid params: "+err.Error())
				return
			}
		}
		id := req.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, stop := hub.Subscribe(params.After)
			defer stop()
			p.respond(id, map[string]any{"seq": hub.Seq()})
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					p.notify("event", map[string]any{"kind": ev.Kind, "event": ev})
				}
			}
		}()
	default:
		p.fail(req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %q", req.Method))
	}
}
