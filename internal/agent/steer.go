package agent

import "strings"

// steering drains the caller's correction channel between turns.
//
// A correction typed while the model is working joins the conversation as a
// user message at the next turn boundary — never in the middle of a tool batch,
// because every tool call must be answered before the next user turn is legal
// on the wire. A channel value that is blank is dropped; a closed channel is
// simply reported as empty forever.
type steering struct {
	ch <-chan string
}

// drain takes everything currently queued without blocking.
func (s *steering) drain() []string {
	if s == nil || s.ch == nil {
		return nil
	}
	var out []string
	for {
		select {
		case text, ok := <-s.ch:
			if !ok {
				s.ch = nil
				return out
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			out = append(out, text)
		default:
			return out
		}
	}
}
