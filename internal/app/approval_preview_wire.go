package app

import (
	"encoding/json"
	"fmt"
	"strings"
)

// approvalPreviewEnv 是「审批差异预览」的独立门控变量。
const approvalPreviewEnv = "ARGENT_APPROVAL_PREVIEW"

func approvalPreviewEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(approvalPreviewEnv)) == "1"
}

// auditSanitizeEnv 是审计文本脱敏的独立门控变量。
const auditSanitizeEnv = "ARGENT_AUDIT_SANITIZE"

func auditSanitizeEnabled(env func(string) string) bool {
	return env != nil && strings.TrimSpace(env(auditSanitizeEnv)) == "1"
}

// approvalDiffPreview 从写类工具的参数里提取 old/new 文本，生成稳定的
// +added/-removed 行数摘要。只处理 edit/multi_edit/write/apply_patch；
// 其它工具返回空串（不附加预览）。它是纯函数，不读全局状态。
func approvalDiffPreview(name string, args json.RawMessage) string {
	var payload struct {
		Old     string `json:"old"`
		New     string `json:"new"`
		Content string `json:"content"`
		Input   string `json:"input"`
	}
	if json.Unmarshal(args, &payload) != nil {
		return ""
	}
	switch name {
	case "edit", "multi_edit":
		// 使用 old/new 字段。
	case "write":
		payload.Old, payload.New = "", payload.Content
	case "apply_patch":
		payload.Old, payload.New = splitPatchStats(payload.Input)
	default:
		return "" // 其它工具（含只读）不附加预览
	}
	oldText, newText := payload.Old, payload.New
	if oldText == "" && newText == "" {
		return ""
	}
	add, del := lineDelta(oldText, newText)
	return fmt.Sprintf("+%d/-%d lines", add, del)
}

// lineDelta 统计从 old 到 new 的新增/删除行数（简单逐行比较，足够做摘要）。
func lineDelta(oldText, newText string) (add, del int) {
	oldLines := strings.Split(strings.TrimRight(oldText, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(newText, "\n"), "\n")
	if oldText == "" {
		oldLines = nil
	}
	if newText == "" {
		newLines = nil
	}
	// 集合差异的近似：新增行 = 在 new 不在 old；删除行 = 在 old 不在 new。
	oldSet := make(map[string]bool, len(oldLines))
	for _, line := range oldLines {
		oldSet[line] = true
	}
	newSet := make(map[string]bool, len(newLines))
	for _, line := range newLines {
		newSet[line] = true
	}
	for _, line := range newLines {
		if !oldSet[line] {
			add++
		}
	}
	for _, line := range oldLines {
		if !newSet[line] {
			del++
		}
	}
	return add, del
}

// splitPatchStats 把 apply_patch 的 input 粗略拆成 old/new 文本（只统计 +/- 行）。
func splitPatchStats(input string) (string, string) {
	var oldb, newb strings.Builder
	for _, line := range strings.Split(input, "\n") {
		if strings.HasPrefix(line, "-") {
			oldb.WriteString(strings.TrimPrefix(line, "-"))
			oldb.WriteByte('\n')
		} else if strings.HasPrefix(line, "+") {
			newb.WriteString(strings.TrimPrefix(line, "+"))
			newb.WriteByte('\n')
		}
	}
	return oldb.String(), newb.String()
}
