package server

import (
	"encoding/json"
	"time"
)

// SessionInfo summarises one stored session for session.list.
type SessionInfo struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Updated time.Time `json:"updated"`
}

// SessionEvent is one record in a session tree. The shape mirrors the JSONL
// session contract (id/parent/kind/ts/data) so the integrator can adapt
// internal/session without reshaping records here.
type SessionEvent struct {
	ID     string          `json:"id,omitempty"`
	Parent string          `json:"parent,omitempty"`
	Kind   string          `json:"kind"`
	TS     time.Time       `json:"ts"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// SessionStore is the read side of session storage used by the RPC and ACP
// transports.
type SessionStore interface {
	List() ([]SessionInfo, error)
	Load(id string) ([]SessionEvent, error)
}
