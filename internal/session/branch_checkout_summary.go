package session

import (
	"encoding/json"
	"os"
	"strings"

	"example.com/argentgo/internal/security"
	"example.com/argentgo/internal/textutil"
)

// branchSummaryEnv 是「分支切换时生成被弃路径摘要」的独立门控变量。
// 默认开启（帮助模型了解被弃路径上做过什么）；仅显式设为 "0"（允许首尾
// 空白）时关闭，关闭时 checkout 与消息投影逐字节不变。
const branchSummaryEnv = "ARGENT_BRANCH_SUMMARY"

func branchSummaryEnabled() bool {
	return strings.TrimSpace(os.Getenv(branchSummaryEnv)) != "0"
}

const branchSummaryMaxRunes = 2000

// branchSummaryForCheckout 直接基于内存中的活动指针与目标 id 计算被弃路径摘要，
// 不依赖文件里「最后一条即活动」的启发式（该启发式与 Checkout 的内存切换不一致）。
// 它返回一条 KindSummary 记录（RetainInThread，不泄漏进子会话），任何失败返回 nil。
func branchSummaryForCheckout(s *Session, entryID string) *Record {
	if !branchSummaryEnabled() || s == nil || entryID == "" || entryID == s.active {
		return nil
	}
	records, err := s.Records()
	if err != nil {
		return nil
	}
	byID := make(map[string]Record, len(records))
	for _, record := range records {
		byID[record.ID] = record
	}
	// 从目标回溯到根，找到与当前活动路径的第一个公共祖先（分叉点）。
	targetAncestors := make(map[string]bool)
	for cur := entryID; cur != ""; {
		targetAncestors[cur] = true
		record, ok := byID[cur]
		if !ok {
			break
		}
		cur = record.Parent
	}
	fork := ""
	for cur := s.active; cur != ""; {
		if targetAncestors[cur] {
			fork = cur
			break
		}
		record, ok := byID[cur]
		if !ok {
			break
		}
		cur = record.Parent
	}
	if fork == "" || fork == s.active {
		return nil // 同一条线性路径，没有真正被放弃的分支。
	}

	// 收集从当前活动叶回溯到分叉点之后（不含）的记录，即被弃路径。
	var abandoned []Record
	for cur := s.active; cur != "" && cur != fork; {
		record, ok := byID[cur]
		if !ok {
			break
		}
		abandoned = append(abandoned, record)
		cur = record.Parent
	}
	if len(abandoned) == 0 {
		return nil
	}

	var text strings.Builder
	text.WriteString("## 已放弃的分支\n")
	for i := len(abandoned) - 1; i >= 0; i-- {
		clean := security.Redact(textOf(abandoned[i].Data))
		if clean == "" {
			continue
		}
		text.WriteString("- ")
		text.WriteString(clean)
		text.WriteByte('\n')
	}
	summary := textutil.ClipRunes(text.String(), branchSummaryMaxRunes)
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(strings.TrimPrefix(summary, "## 已放弃的分支")) == "" {
		return nil
	}
	payload, err := json.Marshal(map[string]string{"text": summary})
	if err != nil {
		return nil
	}
	data, err := SetRetention(payload, RetainInThread)
	if err != nil {
		return nil
	}
	return &Record{Kind: KindSummary, Data: data}
}

// appendBranchSummary 把摘要记录追加到当前活动分支。AppendRecord 会把 parent
// 指向切换后的新叶，使摘要进入新分支的模型可见视图。
func appendBranchSummary(s *Session, record *Record) {
	if s == nil || record == nil {
		return
	}
	_, _ = s.AppendRecord(record.Kind, json.RawMessage(record.Data))
}
