package app

import "testing"

func TestApplyMagicKeywordsWire(t *testing.T) {
	baseSystem := "You are a coding assistant."
	tests := []struct {
		name     string
		prompt   string
		env      func(string) string
		wantSame bool // 系统提示逐字节不变
		wantHas  string
	}{
		{
			name:     "disabled by default",
			prompt:   "ultrathink about this bug",
			env:      func(string) string { return "" },
			wantSame: true,
		},
		{
			name:     "nil env disabled",
			prompt:   "ultrathink about this bug",
			env:      nil,
			wantSame: true,
		},
		{
			name:   "prose keyword injects",
			prompt: "Please ultrathink about this bug",
			env: func(k string) string {
				if k == magicKeywordsEnv {
					return "1"
				}
				return ""
			},
			wantHas: "Think harder",
		},
		{
			name:   "keyword inside code block does not inject",
			prompt: "Run this:\n```go\n// ultrathink\n```",
			env: func(k string) string {
				if k == magicKeywordsEnv {
					return "1"
				}
				return ""
			},
			wantSame: true,
		},
		{
			name:   "keyword in path does not inject",
			prompt: "open ./src/ultrathink.go",
			env: func(k string) string {
				if k == magicKeywordsEnv {
					return "1"
				}
				return ""
			},
			wantSame: true,
		},
		{
			name:   "chillax prose injects",
			prompt: "chillax, just fix the typo",
			env: func(k string) string {
				if k == magicKeywordsEnv {
					return "1"
				}
				return ""
			},
			wantHas: "Keep it brief",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyMagicKeywords(baseSystem, tt.prompt, tt.env)
			if tt.wantSame {
				if got != baseSystem {
					t.Fatalf("system changed when it should not: %q", got)
				}
				return
			}
			if got == baseSystem {
				t.Fatalf("system unchanged: %q", got)
			}
			if tt.wantHas != "" && !containsFold(got, tt.wantHas) {
				t.Fatalf("system %q missing %q", got, tt.wantHas)
			}
		})
	}
}

func TestMagicKeywordsEnabled(t *testing.T) {
	tests := []struct {
		name string
		env  func(string) string
		want bool
	}{
		{"nil", nil, false},
		{"empty", func(string) string { return "" }, false},
		{"one", func(k string) string {
			if k == magicKeywordsEnv {
				return "1"
			}
			return ""
		}, true},
		{"spaces", func(k string) string {
			if k == magicKeywordsEnv {
				return " 1 "
			}
			return ""
		}, true},
		{"other", func(k string) string {
			if k == magicKeywordsEnv {
				return "true"
			}
			return ""
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := magicKeywordsEnabled(tt.env); got != tt.want {
				t.Fatalf("enabled = %v, want %v", got, tt.want)
			}
		})
	}
}

func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFoldASCII(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}
