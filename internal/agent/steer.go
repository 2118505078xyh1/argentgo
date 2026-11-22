package agent

import "strings"

// followUpNoticePrefix 是 follow-up 消息注入时的公告前缀。
const followUpNoticePrefix = "follow-up: "

// steering 在轮次之间抽取调用方的纠正消息通道。
//
// 模型工作期间键入的纠正消息会在下一个轮次边界作为用户消息加入对话——绝不会插在工具批次中间，
// 因为在线协议规定每个工具调用都必须先得到回应，下一个用户轮次才是合法的。
// 空白的通道值会被丢弃；已关闭的通道则永远报告为空。
type steering struct {
	ch <-chan string
}

// Pending 报告当前排队等待的纠正消息数量，但不消费它们。
// nil 接收者或 nil 通道没有任何待处理项。前端用它渲染"N 条排队"的提示，
// 而无需清空 steering 通道。
func (s *steering) Pending() int {
	if s == nil {
		return 0
	}
	return len(s.ch)
}

// drain 无阻塞地取出当前排队的所有内容。
func (s *steering) drain() []string {
	if s == nil || s.ch == nil {
		return nil
	}
	var out []string
	for {
		select {
		case text, ok := <-s.ch:
			if !ok {
				s.ch = nil
				return out
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			out = append(out, text)
		default:
			return out
		}
	}
}
