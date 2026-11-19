package gui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// storageRoot resolves the per-user storage directory: an explicit Options.StorageDir
// wins, otherwise ~/.argent. Keeping this at package scope lets both the bridge
// and the stdlib-backed default backends agree on where files live.
func storageRoot(dir string) string {
	if trimmed := strings.TrimSpace(dir); trimmed != "" {
		return trimmed
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".argent"
	}
	return filepath.Join(home, ".argent")
}

// redactSecrets replaces credential-shaped substrings with placeholders. It is
// a deliberately small stdlib-only sibling of internal/security.Redact: the gui
// package may not import other internals, but snippets must never leak a key.
var (
	secretTokenPattern = regexp.MustCompile(`(?i)(?:sk-(?:proj-|ant-|or-)?|gh[pousr]_|github_pat_|AKIA|ASIA|AIza|xox[abps]-|r8_|hf_|glpat-|npm_|pypi-|eyJ)[A-Za-z0-9._+/\-]{6,}`)
	secretValuePattern = regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|secret|password|passwd|token|access[_-]?key|private[_-]?key)\b\s*[:=]\s*["']?([^\s"'},;]{3,})`)
)

func redactSecrets(text string) string {
	text = secretTokenPattern.ReplaceAllString(text, "[redacted:secret]")
	return secretValuePattern.ReplaceAllString(text, "${1}=[redacted:secret]")
}
