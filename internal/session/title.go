package session

import (
	"strings"

	"github.com/2118505078xyh1/argentgo/internal/security"
)

// TitleRecord overrides a generated session title. An empty title clears the
// override so the next read derives one from the first prompt again.
type TitleRecord struct {
	Title string `json:"title"`
}

// TitleFromPrompt derives a short, single-line title from a prompt without a
// model call or a network request. Secrets are redacted before truncation so a
// placeholder can never be cut in half.
func TitleFromPrompt(prompt string) string {
	return truncateRunes(security.Redact(prompt), titleLimit)
}

// SetTitle appends an explicit title override to the session.
func (s *Session) SetTitle(title string) error {
	return s.Append(KindTitle, TitleRecord{Title: strings.TrimSpace(title)})
}

// Title reports the effective title: the newest override, then the first
// prompt, then the session id.
func (s *Session) Title() string {
	records, err := s.Records()
	if err != nil {
		return s.ID()
	}
	return titleFromRecords(s.ID(), records)
}

// titleFromRecords derives the title used by both list views and the session
// handle. Header/name records win over a prompt-derived title, and the newest
// override wins over an older one.
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
