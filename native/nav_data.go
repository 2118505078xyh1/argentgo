package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// 这里只保留侧边栏/头部仍需的数据结构与解析函数。导航占位面板在
// 2026-11-25 按用户要求移除，聊天是唯一界面；但状态徽章仍读取本轮的
// usage 事件计数，所以 navUsage 保留。todo 与 git 分支解析也随之保留，
// 供后续状态展示复用。

// navUsage 保存从 engine 的 usage 事件读取的真实用量。TotalUsage 是
// 本轮运行的累计值，因此直接覆盖即可，不需要 native 自己累计。
type navUsage struct {
	input  int
	output int
	cached int
	calls  int
}

// navTodoItem 是 todo 工具写入会话的一条待办（native 只解码 JSON-RPC 数据，
// 不复制工具实现）。
type navTodoItem struct {
	text   string
	status string
}

// parsePlanTodos 解码 todo 工具调用参数中的结构化任务列表。
// 只有显式带 todos 数组的 write 调用才算一次计划更新；read 调用
// 或畸形参数返回 nil，调用方据此保留旧列表而不是清空它。
func parsePlanTodos(args string) []navTodoItem {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	var doc struct {
		Todos []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
		} `json:"todos"`
	}
	if json.Unmarshal([]byte(args), &doc) != nil || doc.Todos == nil {
		return nil
	}
	items := make([]navTodoItem, 0, len(doc.Todos))
	for _, t := range doc.Todos {
		text := strings.TrimSpace(t.Text)
		if text == "" {
			continue
		}
		items = append(items, navTodoItem{text: text, status: strings.TrimSpace(t.Status)})
	}
	return items
}

// navGitBranch 读取工作区的 .git/HEAD 并返回当前分支名。readFile 可注入
// 以便离线测试；路径以 root 为工作区根。detached HEAD 不猜分支名，返回空串。
func navGitBranch(root string, readFile func(string) ([]byte, error)) string {
	if readFile == nil {
		readFile = os.ReadFile
	}
	data, err := readFile(filepath.Join(root, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	return branchFromHEAD(string(data))
}

// branchFromHEAD 从 .git/HEAD 的内容解析分支名。
func branchFromHEAD(head string) string {
	line := strings.TrimSpace(head)
	const prefix = "ref: refs/heads/"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	branch := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if branch == "" || strings.ContainsAny(branch, "\r\n") {
		return ""
	}
	return branch
}
