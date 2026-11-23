package session

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/textutil"
)

// 预算投影接线（opt-in，默认关闭）。Messages() 经由 messageEntries 获得条目，
// 因此接线点放在 messageEntries 里：关闭时只多一次环境变量读取，返回值逐字节不变；
// 开启时在工具配对协调之前做投影，使"活尾工具调用"仍是未配对状态，从而能真正触发保护。
const (
	// budgetProjectionEnv 为总开关；仅值为 "1"（允许首尾空白）时启用投影。
	budgetProjectionEnv = "ARGENT_BUDGET_PROJECTION"
	// budgetProjectionBudgetEnv 直接给出投影预算（run E 数）；非正或不可解析时回退到保守默认。
	budgetProjectionBudgetEnv = "ARGENT_BUDGET_PROJECTION_BUDGET"
	// defaultBudgetProjectionBudget 是拿不到 contextWindow/配置时的保守默认预算。
	defaultBudgetProjectionBudget = 32000
	// toolPruneEnv 为工具结果压力裁剪的总开关。默认开启；仅显式设为 "0"
	// （允许首尾空白）时关闭，关闭时消息流逐字节不变。
	toolPruneEnv = "ARGENT_TOOL_PRUNE"
)

// budgetProjectionEnabled 报告预算投影是否被显式启用。默认关闭，逐字节保持旧行为。
func budgetProjectionEnabled() bool {
	return strings.TrimSpace(os.Getenv(budgetProjectionEnv)) == "1"
}

// toolPruneEnabled 报告工具结果压力裁剪是否启用。默认开启，=0 关闭。
func toolPruneEnabled() bool {
	return strings.TrimSpace(os.Getenv(toolPruneEnv)) != "0"
}

// budgetProjectionBudget 返回本次投影使用的 rune 预算及其来源。
//
// 来源说明：Session/Messages 的签名不携带 Options/config，投影路径拿不到模型的
// contextWindow，因此预算只能来自两处——固定的环境变量 ARGENT_BUDGET_PROJECTION_BUDGET
// （来源字符串 "env:ARGENT_BUDGET_PROJECTION_BUDGET"），或拿不到时的保守默认 32000
// （来源字符串 "default:32000"）。这里显式回报来源，便于审计与测试断言。
func budgetProjectionBudget() (int, string) {
	if raw := strings.TrimSpace(os.Getenv(budgetProjectionBudgetEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n, "env:" + budgetProjectionBudgetEnv
		}
	}
	return defaultBudgetProjectionBudget,
		fmt.Sprintf("default:%d", defaultBudgetProjectionBudget)
}

// defaultBudgetProjectionPolicy 是 Messages() 投影使用的默认保留策略：
// 保护最新用户输入、保护未配对的活尾工具调用、清除保留消息的思考文本。
func defaultBudgetProjectionPolicy() ProjectionPolicy {
	return ProjectionPolicy{
		KeepRecentUser:   true,
		KeepLiveToolTail: true,
		DropThinking:     true,
	}
}

// projectBudgetMessages 用默认预算与策略对消息做投影，返回完整结果（含动作与告警）。
// 它是 Messages() 接线与测试共用的入口：Messages() 只消费 Messages 字段，
// 而告警（如"预算不可达"）由 Project 原样返回，测试据此断言。
func projectBudgetMessages(entries []Entry) (ProjectionResult, []ProjectionAction, []ProjectionWarning) {
	budget, _ := budgetProjectionBudget()
	return Project(entries, budget, defaultBudgetProjectionPolicy())
}

func encodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("session: encode payload: %w", err)
	}
	return data, nil
}

// messageEntry 将一条记录投影到对话上；当记录是元数据、事件或压缩标记时返回 false。
func messageEntry(record Record) (Entry, bool) {
	entry := Entry{
		ID:     record.ID,
		Parent: record.Parent,
		TS:     record.TS,
		Data:   record.Data,
		Name:   fieldString(record.Data, "name", "tool"),
	}
	switch record.Kind {
	case KindUser, "prompt":
		entry.Role = "user"
		entry.Content = textOf(record.Data)
	case KindAssistant, "model", "reply":
		entry.Role = "assistant"
		entry.Content = textOf(record.Data)
		entry.ToolCallID = fieldString(record.Data, "toolCallId", "tool_call_id")
		entry.Reasoning = reasoningOf(record.Data)
		entry.ReasoningSignature = signatureOf(record.Data)
	case KindTool, "tool_result", "tool-result", "toolresult":
		entry.Role = "tool"
		entry.Content = firstNonEmpty(
			fieldString(record.Data, "output"),
			fieldString(record.Data, "content"),
			fieldString(record.Data, "result"),
			textOf(record.Data),
		)
		entry.ToolCallID = firstNonEmpty(
			fieldString(record.Data, "toolCallId", "tool_call_id"),
			fieldString(record.Data, "id"),
		)
	case KindMessage:
		entry.Role = fieldString(record.Data, "role")
		if entry.Role == "" {
			entry.Role = "user"
		}
		entry.Content = textOf(record.Data)
		entry.ToolCallID = fieldString(record.Data, "toolCallId", "tool_call_id")
		if entry.Role == "assistant" {
			entry.Reasoning = reasoningOf(record.Data)
			entry.ReasoningSignature = signatureOf(record.Data)
		}
	case KindSummary:
		entry.Role = "system"
		// "summary" 是 Compaction 载荷的规范键。当后续压缩丢弃更早的压缩时，
		// retainedForCompaction 会把它作为携带原始 Compaction Data 的 KindSummary 重新发出 system Entry
		// （entryKind 只能看到角色）。在这里读取 "summary" 可使恢复后的消息文本与被丢弃的 Entry 完全一致；
		// 否则它会变成一条空的系统消息，先前的摘要就会从对话视图中丢失。
		entry.Content = firstNonEmpty(fieldString(record.Data, "text"),
			fieldString(record.Data, "summary"), textOf(record.Data))
	case KindCondensation:
		// 压缩事件是审计元数据，不是对话消息：即使 opt-in 开启了事件落盘，
		// 也绝不让它进入模型可见的消息流。
		return Entry{}, false
	default:
		return Entry{}, false
	}
	if entry.Role == "" {
		return Entry{}, false
	}
	return entry, true
}

func messageEntries(records []Record) []Entry {
	out := make([]Entry, 0, len(records))
	for _, record := range records {
		if entry, ok := messageEntry(record); ok {
			out = append(out, entry)
		}
	}
	// 非破坏性上下文编辑：投影阶段按 target_id 覆盖/隐藏条目（last-wins）。
	// 没有 context_edit 记录时 edits 为空，applyContextEdits 直接返回，逐字节不变。
	// 注意这里传 nil session：messageEntries 仅拿到 records 切片，分支过滤由调用
	// 方在 Records()/Messages() 处已完成的 activePath 保证（见下）。
	out = applyContextEdits(out, contextEditsIn(nil, records))
	// 工具结果压力裁剪（默认开启，=0 关闭）：必须在预算投影之前执行，顺序固定为
	// prune → project → ReconcileToolPairs。原因：Project 按 rune 计算预算，
	// 先缩短超长的工具结果才能让它按裁剪后的 rune 参与预算；且 prune 幂等，
	// 叠加投影时先裁剪可避免投影在裁剪标记中间再次截断。预算复用投影的来源
	// （ARGENT_BUDGET_PROJECTION_BUDGET / 默认 32000）。关闭时不产生任何差异。
	if toolPruneEnabled() {
		budget, _ := budgetProjectionBudget()
		out, _ = PruneToolResults(out, budget)
	}
	// 可选预算投影（默认关闭）：必须在 ReconcileToolPairs 之前投影，
	// 否则孤儿调用已被补上合成结果，KeepLiveToolTail 便无法识别未配对的活尾。
	// 关闭时直接走原有的 ReconcileToolPairs，返回值逐字节不变。
	if budgetProjectionEnabled() {
		result, _, _ := projectBudgetMessages(out)
		out = result.Messages
	}
	return ReconcileToolPairs(out)
}

// reasoningOf 从 assistant 记录中读取模型的思考文本。中性的 "reasoning" 键和
// OpenAI 兼容的 "reasoning_content" 键都被接受，使导入的转录也能保留其推理内容。
// 两个键都不存在的记录只会得到空字符串，从而使旧会话保持不变。
func reasoningOf(raw json.RawMessage) string {
	return fieldString(raw, "reasoning", "reasoning_content")
}

// signatureOf 从 assistant 记录中读取思考文本的签名。它既接受 llm.Message
// 写入的中性 "reasoning_signature" 键，也接受归档使用的 "ReasoningSignature"
// 键，使旧记录与显式写入的记录都能被正确投影；两键都不存在时返回空字符串。
func signatureOf(raw json.RawMessage) string {
	return fieldString(raw, "reasoning_signature", "ReasoningSignature")
}

// AssistantRecord 为携带推理文本的 assistant 消息构建持久化载荷。空字段会被省略，
// 使较早版本写入的记录保持逐字节一致。当调用方有推理内容需要保留时，
// 可将其与 Session.Append(KindAssistant, ...) 一起使用；agent 循环的 llm.Message
// 载荷已经携带相同的 "reasoning" 键。
func AssistantRecord(content, reasoning string) map[string]any {
	payload := make(map[string]any, 2)
	if content != "" {
		payload["content"] = content
	}
	if reasoning != "" {
		payload["reasoning"] = reasoning
	}
	return payload
}

// AssistantRecordWithSignature 在 AssistantRecord 的基础上携带推理签名。
// 空字段同样会被省略，使不带签名的写入保持旧字节不变。
func AssistantRecordWithSignature(content, reasoning, signature string) map[string]any {
	payload := AssistantRecord(content, reasoning)
	if signature != "" {
		payload["reasoning_signature"] = signature
	}
	return payload
}

// fieldString 从 JSON 对象载荷中读取第一个匹配的字符串键。
func fieldString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}

// textOf 读取消息载荷：裸 JSON 字符串，或对象常用的文本键
// （`content`、`text`、`output`、`result`、`message`）。
func textOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return ""
	}
	for _, key := range []string{"content", "text", "output", "result", "message"} {
		if text, ok := object[key].(string); ok {
			return text
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// truncateRunes 将文本裁剪到 limit 个符文，先将其折叠为一行。
// 切点交给 textutil.ClipRunes 吸附到字素簇边界，因此组合序列、
// ZWJ emoji 与旗帜不会被从中间劈开；符文预算与 TrimSpace 语义不变。
func truncateRunes(text string, limit int) string {
	cleaned := strings.Join(strings.Fields(strings.ReplaceAll(text, "\n", " ")), " ")
	if limit <= 0 {
		return ""
	}
	// 纯计数用 RuneCountInString，避免 ClipRunes 之前先做一次无用的 []rune 全量分配。
	if utf8.RuneCountInString(cleaned) <= limit {
		return cleaned
	}
	return strings.TrimSpace(textutil.ClipRunes(cleaned, limit))
}

func firstLine(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncateRunes(trimmed, limit)
		}
	}
	return ""
}
