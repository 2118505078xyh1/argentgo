package session

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// 本文件实现非破坏性的上下文编辑：追加一条 kind=context_edit 记录，
// 在消息投影时按 target_id 覆盖目标条目的可见文本，而不改动任何原始记录字节。
// 这与 pi-mono 的 context_edit 思想一致（targetId + replacement、last-wins、
// 原条目保留用于 UI/导出/计费），但完全自研。

// KindContextEdit 是非破坏性上下文编辑记录的 kind。
// 未知 kind 会被 Store 逐字保留并被对话视图忽略，因此旧会话逐字节不变。
const KindContextEdit = "context_edit"

const (
	contextEditMaxTargetRunes = 128
	contextEditMaxTextRunes   = 32 << 10 // 32 KiB 文本替换上限
)

// ContextEdit 是投影时应用的一次覆盖。
// TargetID 指向目标条目的记录 ID；Text 为替换文本，空串表示隐藏该条目。
type ContextEdit struct {
	TargetID string `json:"target_id"`
	Text     string `json:"text"`
}

// sanitizeContextEdit 对编辑载荷脱敏并限制大小，使调用方可传入原始文本。
// 与 sanitizeProgress 同范式：security.Redact + rune 上限。
func sanitizeContextEdit(e ContextEdit) ContextEdit {
	e.TargetID = strings.TrimSpace(e.TargetID)
	if utf8.RuneCountInString(e.TargetID) > contextEditMaxTargetRunes {
		e.TargetID = capContextEditRunes(e.TargetID, contextEditMaxTargetRunes)
	}
	e.Text = security.Redact(e.Text)
	if utf8.RuneCountInString(e.Text) > contextEditMaxTextRunes {
		e.Text = capContextEditRunes(e.Text, contextEditMaxTextRunes)
	}
	return e
}

func capContextEditRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:limit]))
}

// AppendContextEdit 追加一条上下文编辑记录，仅返回写入错误。
// 载荷先脱敏再落盘；编辑写入活动分支，因此 fork/checkout 后归属自然正确。
func (s *Session) AppendContextEdit(e ContextEdit) error {
	if e.TargetID == "" {
		return fmt.Errorf("session: context edit target id must not be empty")
	}
	_, err := s.AppendRecord(KindContextEdit, sanitizeContextEdit(e))
	return err
}

// contextEditsIn 从记录中收集活动分支上的编辑映射。
// 同一 target 多次编辑时后写覆盖先写（last-wins）；扫描活动分支使 fork
// 后的编辑只影响该分支自己的投影，不污染父分支（分支相对）。
func contextEditsIn(s *Session, records []Record) map[string]ContextEdit {
	scope := records
	if s != nil {
		if path := s.activePath(records); len(path) > 0 {
			scope = path
		}
	}
	edits := make(map[string]ContextEdit)
	for _, rec := range scope {
		if rec.Kind != KindContextEdit {
			continue
		}
		var e ContextEdit
		if err := json.Unmarshal(rec.Data, &e); err != nil || e.TargetID == "" {
			continue
		}
		// 后写覆盖先写：直接赋值即可（records 按文件顺序，后者更晚）。
		edits[e.TargetID] = e
	}
	return edits
}

// applyContextEdits 把编辑映射应用到投影出的消息条目上：
//   - 有替换文本：改写 Content（同时清空 Reasoning，避免旧思考与新文本矛盾）
//   - 空文本：隐藏该条目（从可见消息中移除）
//
// 编辑只影响模型可见的投影，Entry.ID/Parent 与原记录保持，不触碰磁盘字节。
func applyContextEdits(entries []Entry, edits map[string]ContextEdit) []Entry {
	if len(edits) == 0 {
		return entries
	}
	out := entries[:0]
	for _, entry := range entries {
		edit, ok := edits[entry.ID]
		if !ok {
			out = append(out, entry)
			continue
		}
		if edit.Text == "" {
			continue // 隐藏：不进入模型可见投影
		}
		entry.Content = edit.Text
		entry.Reasoning = ""
		entry.ReasoningSignature = ""
		out = append(out, entry)
	}
	return out
}
