package i18n

import "testing"

func TestFormatPromptWithLanguage(t *testing.T) {
	cases := []struct {
		name     string
		template string
		lang     string
		vars     map[string]string
		want     string
	}{
		{
			name:     "language slot uses Chinese hint",
			template: "请回答。{language}",
			lang:     ZhCN,
			want:     "请回答。请用中文回复",
		},
		{
			name:     "language slot uses English hint",
			template: "Answer briefly. {language}",
			lang:     EnUS,
			want:     "Answer briefly. Reply in English",
		},
		{
			name:     "CJK locale alias resolves to Chinese hint",
			template: "请回答。{language}",
			lang:     "中文",
			want:     "请回答。请用中文回复",
		},
		{
			name:     "other variables are substituted from vars",
			template: "Hi {name}, do {task}. {language}",
			lang:     EnUS,
			vars:     map[string]string{"name": "Alex", "task": "the work"},
			want:     "Hi Alex, do the work. Reply in English",
		},
		{
			name:     "unknown slot is preserved",
			template: "{language} and {missing}",
			lang:     EnUS,
			want:     "Reply in English and {missing}",
		},
		{
			name:     "empty lang empties the language slot",
			template: "请回答。{language}",
			lang:     "",
			want:     "请回答。",
		},
		{
			name:     "unsupported lang empties the language slot",
			template: "Answer. {language}",
			lang:     "fr-FR",
			want:     "Answer. ",
		},
		{
			name:     "CJK template and values are preserved",
			template: "请用 {tool} 处理：{language}",
			lang:     ZhCN,
			vars:     map[string]string{"tool": "文件"},
			want:     "请用 文件 处理：请用中文回复",
		},
		{
			name:     "multiple slots including repeated language",
			template: "{language} | {name} | {language}",
			lang:     ZhCN,
			vars:     map[string]string{"name": "世界"},
			want:     "请用中文回复 | 世界 | 请用中文回复",
		},
		{
			name:     "vars language key does not override lang",
			template: "{language}",
			lang:     ZhCN,
			vars:     map[string]string{"language": "覆盖"},
			want:     "请用中文回复",
		},
		{
			name:     "escaped braces stay literal while live slot expands",
			template: "{{language}} {language}",
			lang:     EnUS,
			want:     "{language} Reply in English",
		},
		{
			name:     "nil vars keeps unknown slots",
			template: "{language} {name}",
			lang:     EnUS,
			want:     "Reply in English {name}",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatPromptWithLanguage(tc.template, tc.lang, tc.vars); got != tc.want {
				t.Errorf("formatPromptWithLanguage(%q, %q, %v) = %q, want %q",
					tc.template, tc.lang, tc.vars, got, tc.want)
			}
		})
	}
}

func TestFormatPromptWithLanguageIsPure(t *testing.T) {
	ResetMissing()
	vars := map[string]string{"name": "X"}
	first := formatPromptWithLanguage("{language} {name} {missing}", ZhCN, vars)
	second := formatPromptWithLanguage("{language} {name} {missing}", ZhCN, vars)
	if first != second || first != "请用中文回复 X {missing}" {
		t.Errorf("not deterministic: %q then %q", first, second)
	}
	if len(vars) != 1 || vars["name"] != "X" {
		t.Errorf("vars map was mutated: %#v", vars)
	}
	if got := Missing(); len(got) != 0 {
		t.Errorf("formatPromptWithLanguage recorded missing keys: %#v", got)
	}
}
