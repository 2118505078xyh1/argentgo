package session

import "fmt"

// UserMessage 是会话树中一条用户消息的只读摘要，供 fork 对话框列出历史提问。
// ID 是记录 id（用作 ForkBefore 的分叉点），Text 是当时的 prompt 文本。
type UserMessage struct {
	ID   string
	Text string
}

// UserMessages 按文件顺序列出整棵会话树中的所有用户消息。
// 它只读：不修改活动指针、不追加记录；也不局限于活动分支，因此已废弃分支上的
// 历史提问同样会列出，可作为 fork 的候选点。读取失败时返回 nil。
func (s *Session) UserMessages() []UserMessage {
	records, err := s.Records()
	if err != nil {
		return nil
	}
	out := make([]UserMessage, 0, len(records))
	for _, record := range records {
		entry, ok := messageEntry(record)
		if !ok || entry.Role != "user" {
			continue
		}
		out = append(out, UserMessage{ID: record.ID, Text: entry.Content})
	}
	return out
}

// ForkBefore 在 messageID 指向的用户消息处分叉出新会话，返回新会话 id
// 以及该消息当时的文本（供 UI 还原到 composer）。
//
// 它复用 Session.Fork 的语义：复制从根到该记录的路径、保留原有记录 id、
// 不改动原会话。messageID 必须是会话树中真实存在的用户消息；空 id、
// 未知 id、环以及指向非用户消息的记录都会被拒绝，失败时不产生新会话文件。
func (s *Session) ForkBefore(messageID string) (newID string, restoredPrompt string, err error) {
	if messageID == "" {
		return "", "", fmt.Errorf("session: empty message id")
	}
	records, err := s.Records()
	if err != nil {
		return "", "", err
	}
	found := false
	for _, record := range records {
		if record.ID != messageID {
			continue
		}
		entry, ok := messageEntry(record)
		if !ok || entry.Role != "user" {
			return "", "", fmt.Errorf("session: message %q is not a user message", messageID)
		}
		restoredPrompt = entry.Content
		found = true
		break
	}
	if !found {
		return "", "", fmt.Errorf("session: unknown message %q", messageID)
	}
	fork, err := s.Fork(messageID)
	if err != nil {
		return "", "", err
	}
	return fork.ID(), restoredPrompt, nil
}
