package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"example.com/argentgo/internal/tools"
)

// approvalPreviewEnv 是「审批差异预览」的独立门控变量。默认开启，修剪后精确为 "0" 时关闭。
const approvalPreviewEnv = "ARGENT_APPROVAL_PREVIEW"

func approvalPreviewEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env(approvalPreviewEnv)) != "0"
}

// auditSanitizeEnv 是审计文本脱敏的独立门控变量。默认开启（安全默认值，
// 守护「脱敏发生在内容写入审计日志之前」不变量），仅 =0 紧急回退。
const auditSanitizeEnv = "ARGENT_AUDIT_SANITIZE"

func auditSanitizeEnabled(env func(string) string) bool {
	return env == nil || strings.TrimSpace(env(auditSanitizeEnv)) != "0"
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

// approvalSnapshots 从写类工具的参数中提取编辑前后的文本快照，键为文件路径。
// 字段约定与 approvalDiffPreview 完全一致（edit/multi_edit 的 old/new、write 的
// content、apply_patch 的 input），因此两处对同一调用给出一致的统计口径。
//
// 安全链传给 Approver 的是「展示用参数」：路径可能被解析成真实位置，末尾还可能
// 附带脱敏摘要。这里用 Decoder 只读取第一个 JSON 值，从而忽略末尾附加文本；
// 参数不是 JSON 对象或没有可用文本时返回 nil，调用方据此跳过 diff 渲染。
func approvalSnapshots(name string, args string) (map[string]string, map[string]string) {
	dec := json.NewDecoder(strings.NewReader(args))
	dec.UseNumber()
	var payload struct {
		Path     string `json:"path"`
		File     string `json:"file"`
		FilePath string `json:"filepath"`
		Old      string `json:"old"`
		New      string `json:"new"`
		Content  string `json:"content"`
		Input    string `json:"input"`
		Edits    []struct {
			Old     string `json:"old"`
			New     string `json:"new"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		} `json:"edits"`
	}
	if err := dec.Decode(&payload); err != nil {
		return nil, nil
	}
	path := firstNonEmpty(payload.Path, payload.File, payload.FilePath)
	before := make(map[string]string, 1)
	after := make(map[string]string, 1)

	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edit":
		if path == "" || (payload.Old == "" && payload.New == "") {
			return nil, nil
		}
		before[path], after[path] = payload.Old, payload.New
	case "multi_edit":
		if path == "" || len(payload.Edits) == 0 {
			return nil, nil
		}
		var oldText, newText strings.Builder
		for _, edit := range payload.Edits {
			old, next := edit.Old, edit.New
			if old == "" {
				old = edit.OldText
			}
			if next == "" {
				next = edit.NewText
			}
			oldText.WriteString(old)
			newText.WriteString(next)
		}
		before[path], after[path] = oldText.String(), newText.String()
	case "write":
		if path == "" || payload.Content == "" {
			return nil, nil
		}
		before[path], after[path] = "", payload.Content
	case "apply_patch":
		if strings.TrimSpace(payload.Input) == "" {
			return nil, nil
		}
		actions, err := tools.ParseApplyPatch(payload.Input)
		if err != nil {
			return nil, nil
		}
		for _, action := range actions {
			if target := strings.TrimSpace(action.Path); target != "" {
				before[target], after[target] = action.OldText(), action.NewText()
			}
		}
	default:
		return nil, nil
	}
	if len(before) == 0 {
		return nil, nil
	}
	return before, after
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
