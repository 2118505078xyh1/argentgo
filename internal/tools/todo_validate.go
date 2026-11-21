package tools

import (
	"fmt"
	"strconv"
	"strings"
)

// todo 状态枚举。注意：todoTool.Run 目前只接受前三种（空值归一为 pending），
// 这里把 cancelled 也纳入合法集合，是为 opt-in 校验准备的第四种状态；
// 是否放开写路径由主会话后续决定，本文件不改动默认行为。
const (
	todoStatusPending    = "pending"
	todoStatusInProgress = "in_progress"
	todoStatusCompleted  = "completed"
	todoStatusCancelled  = "cancelled"
)

// IssueKind 对 todo 状态校验问题分类。
type IssueKind string

const (
	// IssueInvalidStatus 表示状态不在允许的四种枚举内。
	IssueInvalidStatus IssueKind = "invalid-status"
	// IssueMultipleInProgress 表示存在多个 in_progress 任务。
	IssueMultipleInProgress IssueKind = "multiple-in-progress"
	// IssueMissingVerification 表示列表全部完成但没有任何验证记录。
	IssueMissingVerification IssueKind = "missing-verification"
)

// Issue 是 ValidateTodoState 发现的一个状态不合规问题。
// Index 为 items 中从零开始的下标；整体性问题（多个 in_progress、缺少验证）
// 使用 -1。Status/Value 保留触发问题的原始值，便于调用方诊断。
type Issue struct {
	Kind    IssueKind
	Index   int
	Status  string
	Value   string
	Message string
}

// String 返回可读的诊断文本。
func (i Issue) String() string {
	if i.Message != "" {
		return i.Message
	}
	return string(i.Kind)
}

// ValidateTodoState 检查一份任务列表是否满足 todo 纪律的不变量，返回发现的
// 全部问题（无问题时返回空切片）。它是纯函数：不读写文件、不修改 items，也
// 不接入 todo 工具的写路径，因此不改变默认行为或持久化字节；调用方可自行
// 决定在何处启用。
//
// 当前检查三条不变量：
//   - 状态必须恰好是 pending / in_progress / completed / cancelled 之一；
//   - 至多一个 in_progress；
//   - 非空列表全部为 completed 时，至少有一条带非空 Verification 验证记录。
//
// 校验按状态严格匹配：空字符串与带空白的状态都视为非法。调用方若想沿用
// Run 的空值归一行为，应先自行归一化再传入。
func ValidateTodoState(items []todoItem) []Issue {
	issues := make([]Issue, 0)
	var inProgress, completed int
	var hasVerification bool

	for i, item := range items {
		switch item.Status {
		case todoStatusPending, todoStatusInProgress, todoStatusCompleted, todoStatusCancelled:
		default:
			issues = append(issues, Issue{
				Kind:    IssueInvalidStatus,
				Index:   i,
				Status:  item.Status,
				Message: fmt.Sprintf("todo: item %d has invalid status %q (want pending, in_progress, completed or cancelled)", i+1, item.Status),
			})
		}
		switch item.Status {
		case todoStatusInProgress:
			inProgress++
		case todoStatusCompleted:
			completed++
		}
		if strings.TrimSpace(item.Verification) != "" {
			hasVerification = true
		}
	}

	if inProgress > 1 {
		issues = append(issues, Issue{
			Kind:    IssueMultipleInProgress,
			Index:   -1,
			Value:   strconv.Itoa(inProgress),
			Message: fmt.Sprintf("todo: %d items are in_progress, want at most one", inProgress),
		})
	}

	if len(items) > 0 && completed == len(items) && !hasVerification {
		issues = append(issues, Issue{
			Kind:    IssueMissingVerification,
			Index:   -1,
			Message: fmt.Sprintf("todo: all %d items are completed but no verification record was provided", completed),
		})
	}

	return issues
}
