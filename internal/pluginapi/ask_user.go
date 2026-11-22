package pluginapi

import "context"

// 本文件定义 ask_user 的跨包类型契约。放在零依赖的 pluginapi 包，使 tools
// （工具实现）与 app（交互实现）可以各自独立 import，不必互相依赖。

// 问题种类：自由文本 / 单选 / 多选。
const (
	UserQuestionKindText     = "text"
	UserQuestionKindSingle   = "single"
	UserQuestionKindMultiple = "multiple"
)

// UserQuestion 是 ask_user 工具向用户提出的一个问题。
type UserQuestion struct {
	ID      string   `json:"id"`
	Prompt  string   `json:"prompt"`
	Kind    string   `json:"kind"` // text | single | multiple
	Options []string `json:"options,omitempty"`
}

// UserAnswer 是用户对一个问题的回答。ID 与问题的 ID 一一对应。
// Value 用于 text/single，Values 用于 multiple。
type UserAnswer struct {
	ID     string   `json:"id"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

// UserAsker 是 ask_user 工具用于向用户提问的窄接口。
// 接口定义在消费方（pluginapi），实现由 app 注入；tools 不依赖 app。
type UserAsker interface {
	AskUser(ctx context.Context, questions []UserQuestion) ([]UserAnswer, error)
}
