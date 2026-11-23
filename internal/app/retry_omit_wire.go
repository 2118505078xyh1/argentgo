package app

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/session"
)

// retryOmitEnv 是重试前上下文省略的独立门控变量。
const retryOmitEnv = "ARGENT_RETRY_OMIT"

func retryOmitEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(retryOmitEnv)) == "1"
}

// omitLastAssistantAttempt 把活动分支上最近一条 assistant 及其紧随的工具结果
// 以 ContextEdit{TargetID, Text:""} 从模型可见上下文省略（不改磁盘历史）。
// turn 目前仅用于日志语境；省略范围以会话尾部为准，因为重试总是紧跟最近一次失败尝试。
func omitLastAssistantAttempt(sess *session.Session) error {
	if sess == nil {
		return nil
	}
	records, err := sess.Records()
	if err != nil {
		return err
	}
	// 从尾部向前找最近的 assistant 记录，连同其后紧跟的 tool（若有）一起省略。
	lastAssistant := -1
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Kind == session.KindAssistant {
			lastAssistant = i
			break
		}
	}
	if lastAssistant < 0 {
		return nil
	}
	if id := records[lastAssistant].ID; id != "" {
		if err := sess.AppendContextEdit(session.ContextEdit{TargetID: id, Text: ""}); err != nil {
			return err
		}
	}
	// 紧随 assistant 的连续 tool 结果（活动路径上 parent 指向该 assistant）也省略。
	for i := lastAssistant + 1; i < len(records); i++ {
		if records[i].Parent != records[lastAssistant].ID || records[i].Kind != session.KindTool {
			break
		}
		if id := records[i].ID; id != "" {
			if err := sess.AppendContextEdit(session.ContextEdit{TargetID: id, Text: ""}); err != nil {
				return err
			}
		}
	}
	return nil
}
