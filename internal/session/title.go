package session

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// TitleRecord 覆盖生成的会话标题。空标题会清除覆盖，
// 使下次读取时再次从第一条 prompt 派生标题。
type TitleRecord struct {
	Title string `json:"title"`
}

// TitleFromPrompt 从 prompt 派生一个简短的单行标题，无需模型调用或网络请求。
// 密钥会在截断前被脱敏，使占位符永远不会被切成两半。
func TitleFromPrompt(prompt string) string {
	return truncateRunes(security.Redact(prompt), titleLimit)
}

// SetTitle 向会话追加一条显式的标题覆盖。
func (s *Session) SetTitle(title string) error {
	return s.Append(KindTitle, TitleRecord{Title: strings.TrimSpace(title)})
}

// Title 返回有效标题：最新的覆盖值，其次是第一条 prompt，最后是会话 id。
func (s *Session) Title() string {
	records, err := s.Records()
	if err != nil {
		return s.ID()
	}
	return titleFromRecords(s.ID(), records)
}

// titleFromRecords 派生列表视图和会话句柄都使用的标题。header/name 记录
// 优先于从 prompt 派生的标题，而最新的覆盖优先于较旧的覆盖。
func titleFromRecords(id string, records []Record) string {
	derived, explicit := "", ""
	for _, record := range records {
		switch record.Kind {
		case KindHeader:
			if title := strings.TrimSpace(fieldString(record.Data, "title")); title != "" {
				explicit = title
			}
		case KindTitle:
			explicit = strings.TrimSpace(fieldString(record.Data, "title"))
		case KindUser, "prompt":
			if derived == "" {
				derived = TitleFromPrompt(textOf(record.Data))
			}
		case KindMessage:
			if derived == "" && fieldString(record.Data, "role") == "user" {
				derived = TitleFromPrompt(textOf(record.Data))
			}
		}
	}
	if explicit != "" {
		return explicit
	}
	if derived != "" {
		return derived
	}
	return id
}
