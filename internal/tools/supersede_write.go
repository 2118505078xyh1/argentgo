package tools

import (
	"encoding/json"
)

// 本文件给变更型文件工具补上"写操作使同路径的更早读取失效"的回收元数据。
// 这是 omp 的 pruneSupersededToolResults 中「read A → edit A → 旧 read 失效」
// 的语义缺口：此前只有 read 工具产生 SupersedeKey，写工具不产生，因此被改动的
// 文件的旧读取内容会一直占着模型上下文。
//
// 实现刻意复用现有 ToolMeta.SupersedeKey：写工具的成功结果携带与 read 相同的
// key（"read|<path>"），reclaimByMeta 的 newestSuccessful 会把更晚的它视为取代者，
// 从而 stub 掉更早的同一路径读取。错误时返回零值（与 read 工具一致），失败写入
// 绝不会抹掉模型可能仍需要的旧内容。无需改动任何类型定义或 agent 逻辑。

// writeSupersedeMeta 从写工具参数解析路径，生成取代键。
func writeSupersedeMeta(args json.RawMessage, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	var a struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ToolMeta{}
	}
	p := normalizeReadPath(a.Path)
	if p == "" {
		return ToolMeta{}
	}
	return ToolMeta{SupersedeKey: "read|" + p}
}

// multiEditSupersedeMeta 处理 multi_edit 的 edits 数组：以第一个编辑的路径
// 作为取代键（ToolMeta 只承载单个键）。多路径场景的其余路径暂不追踪，
// 这是有意的保守取舍——单文件编辑是绝大多数情况。
func multiEditSupersedeMeta(args json.RawMessage, err error) ToolMeta {
	if err != nil {
		return ToolMeta{}
	}
	var a struct {
		Edits []struct {
			Path string `json:"path"`
		} `json:"edits"`
	}
	if json.Unmarshal(args, &a) != nil || len(a.Edits) == 0 {
		return ToolMeta{}
	}
	return writeSupersedeMeta(mustMarshalPath(a.Edits[0].Path), nil)
}

func mustMarshalPath(p string) json.RawMessage {
	data, _ := json.Marshal(map[string]string{"path": p})
	return data
}

// 三个写工具实现 metaTool 契约（Meta 方法）。方法体简短，独立成文件
// 避免改写在途热路径文件（write/edit/multi_edit 的实现文件）。
func (writeTool) Meta(args json.RawMessage, _ string, err error) ToolMeta {
	return writeSupersedeMeta(args, err)
}

func (editTool) Meta(args json.RawMessage, _ string, err error) ToolMeta {
	return writeSupersedeMeta(args, err)
}

func (multiEditTool) Meta(args json.RawMessage, _ string, err error) ToolMeta {
	return multiEditSupersedeMeta(args, err)
}
