package session

import (
	"encoding/json"
	"sort"
	"strings"
)

// KindImageOffload 是图像卸载标记的记录 kind。它按请求顺序（seq + 严格递增的
// occurrence index）指向要省略的图像 occurrence；原始转录字节永不改动。
const KindImageOffload = "image_offload"

// ImageOmittedPlaceholder 是投影时替换图像 part 的有界文本占位。
// 它保留"这里曾有图像"的可检索事实，但不携带任何媒体字节。
const ImageOmittedPlaceholder = "[image omitted]"

// ImageOffload 是一条卸载标记：Index 是请求顺序中从 0 开始的图像 occurrence 序号，
// Seq 是标记写入的单调递增序号，使同一视图下多次标记可稳定排序。
type ImageOffload struct {
	Seq   int `json:"seq"`
	Index int `json:"index"`
}

// ImageOffloadRecord 构造可写入 JSONL 的卸载标记记录（纯函数）。
func ImageOffloadRecord(seq, index int) (Record, error) {
	if seq < 0 || index < 0 {
		return Record{}, errNegativeImageOffload
	}
	data, err := encodePayload(ImageOffload{Seq: seq, Index: index})
	if err != nil {
		return Record{}, err
	}
	return Record{Kind: KindImageOffload, Data: data}, nil
}

// errNegativeImageOffload 防御非法序号，避免标记出负数 occurrence。
var errNegativeImageOffload = errText("session: negative image offload index")

type errText string

func (e errText) Error() string { return string(e) }

// AppendImageOffload 追加一条图像卸载标记到活动分支。
func (s *Session) AppendImageOffload(seq, index int) error {
	record, err := ImageOffloadRecord(seq, index)
	if err != nil {
		return err
	}
	_, err = s.AppendRecord(record.Kind, record.Data)
	return err
}

// imageOccurrence 描述请求顺序中的一个图像 occurrence。
// Entry 是投影条目下标，Part 是 content 数组中的 part 下标（top-level 图像为 -1）。
type imageOccurrence struct {
	Entry int
	Part  int
}

// imageOccurrences 按请求顺序扫描 user/tool 消息，跳过 assistant 的媒体输出，
// 累计每个图像 part。它只读 Entry.Data 中的 JSON，不依赖 llm 包。
func imageOccurrences(entries []Entry) []imageOccurrence {
	var out []imageOccurrence
	for i, entry := range entries {
		if entry.Role != "user" && entry.Role != "tool" {
			continue // 只标记 user/tool，跳过 assistant 与 system。
		}
		if n := topLevelImageCount(entry.Data); n > 0 {
			for p := 0; p < n; p++ {
				out = append(out, imageOccurrence{Entry: i, Part: -1 - p})
			}
		}
		for _, part := range contentImageParts(entry.Data) {
			out = append(out, imageOccurrence{Entry: i, Part: part})
		}
	}
	return out
}

// contentImageParts 返回 entry.Data 的 content 数组中图像 part 的下标列表。
// 同时识别 OpenAI 风格 image_url 与 Anthropic 风格 image 两种 type。
func contentImageParts(raw json.RawMessage) []int {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return nil
	}
	parts, ok := object["content"].([]any)
	if !ok {
		return nil
	}
	var out []int
	for i, part := range parts {
		item, ok := part.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := item["type"].(string)
		if kind == "image_url" || kind == "image" || kind == "input_image" {
			out = append(out, i)
		}
	}
	return out
}

// topLevelImageCount 兼容把图片放在顶层 images 数组的载荷布局。
func topLevelImageCount(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var object map[string]any
	if json.Unmarshal(raw, &object) != nil {
		return 0
	}
	images, ok := object["images"].([]any)
	if !ok {
		return 0
	}
	return len(images)
}

// PlanImageOffload 为最旧的 count 个尚未卸载的 occurrence 生成标记。
// existing 是当前已写入的标记，用于增量卸载时跳过已经卸载过的序号。
// 返回标记按 Seq 升序、Index 升序，结果确定可复现。
func PlanImageOffload(entries []Entry, existing []ImageOffload, count int) []ImageOffload {
	if count <= 0 {
		return nil
	}
	done := make(map[int]bool, len(existing))
	maxSeq := 0
	for _, marker := range existing {
		done[marker.Index] = true
		if marker.Seq > maxSeq {
			maxSeq = marker.Seq
		}
	}
	occurrences := imageOccurrences(entries)
	out := make([]ImageOffload, 0, count)
	for index := range occurrences {
		if done[index] {
			continue
		}
		maxSeq++
		out = append(out, ImageOffload{Seq: maxSeq, Index: index})
		if len(out) >= count {
			break
		}
	}
	return out
}

// ImageOffloadsIn 从活动分支记录中收集卸载标记，按 Seq 排序并去重。
func ImageOffloadsIn(records []Record) []ImageOffload {
	var out []ImageOffload
	for _, record := range records {
		if record.Kind != KindImageOffload {
			continue
		}
		var marker ImageOffload
		if json.Unmarshal(record.Data, &marker) == nil && marker.Index >= 0 {
			out = append(out, marker)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].Index < out[j].Index
	})
	seen := make(map[int]bool, len(out))
	uniq := out[:0]
	for _, marker := range out {
		if !seen[marker.Index] {
			seen[marker.Index] = true
			uniq = append(uniq, marker)
		}
	}
	return uniq
}

// applyImageOffload 把标记指向的图像 occurrence 投影为文本占位。
// 无标记时返回原切片（逐字节不变）；Entry 的身份与其他内容保持不变。
func applyImageOffload(entries []Entry, markers []ImageOffload) []Entry {
	if len(markers) == 0 {
		return entries
	}
	offloaded := make(map[int]bool, len(markers))
	for _, marker := range markers {
		offloaded[marker.Index] = true
	}
	occurrences := imageOccurrences(entries)
	out := append([]Entry(nil), entries...)
	for index, occurrence := range occurrences {
		if !offloaded[index] {
			continue
		}
		entry := &out[occurrence.Entry]
		replaceImageOccurrence(entry, occurrence)
	}
	return out
}

// replaceImageOccurrence 把单个图像 part 改写为文本 part，并同步 Content 文本。
// 解析失败时退化为只改 Content，保证标记永远不会把消息整条吞掉。
func replaceImageOccurrence(entry *Entry, occurrence imageOccurrence) {
	if !strings.Contains(entry.Content, ImageOmittedPlaceholder) {
		if entry.Content == "" {
			entry.Content = ImageOmittedPlaceholder
		} else {
			entry.Content += "\n" + ImageOmittedPlaceholder
		}
	}
	if len(entry.Data) == 0 {
		return
	}
	var object map[string]any
	if json.Unmarshal(entry.Data, &object) != nil {
		return
	}
	placeholder := map[string]any{"type": "text", "text": ImageOmittedPlaceholder}
	if occurrence.Part >= 0 {
		parts, ok := object["content"].([]any)
		if ok && occurrence.Part < len(parts) {
			parts[occurrence.Part] = placeholder
			object["content"] = parts
		}
	} else {
		// 顶层 images 数组：用同长度的文本占位数组替换，保持结构可预测。
		if images, ok := object["images"].([]any); ok {
			slot := -occurrence.Part - 1
			if slot < len(images) {
				images[slot] = ImageOmittedPlaceholder
				object["images"] = images
			}
		}
	}
	if data, err := json.Marshal(object); err == nil {
		entry.Data = data
	}
}
