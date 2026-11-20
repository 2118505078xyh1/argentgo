package server

import (
	"encoding/json"
	"time"
)

// SessionInfo 为 session.list 汇总一条已存储的会话。
type SessionInfo struct {
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Updated time.Time `json:"updated"`
}

// SessionEvent 是会话树中的一条记录。其形态对应 JSONL
// 会话约定（id/parent/kind/ts/data），使集成方可以适配
// internal/session，而无需在这里重塑记录。
type SessionEvent struct {
	ID     string          `json:"id,omitempty"`
	Parent string          `json:"parent,omitempty"`
	Kind   string          `json:"kind"`
	TS     time.Time       `json:"ts"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// SessionStore 是 RPC 和 ACP 传输使用的会话存储的
// 读取侧。
type SessionStore interface {
	List() ([]SessionInfo, error)
	Load(id string) ([]SessionEvent, error)
}
