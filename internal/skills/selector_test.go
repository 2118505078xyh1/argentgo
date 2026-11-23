package skills

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRankSkillsFieldedBM25(t *testing.T) {
	docs := []FieldedDoc{
		{Name: "git_commit", Short: "commit staged changes", Body: "run git commit with a message"},
		{Name: "note", Short: "take a note", Body: "git commit is mentioned only in this long body as an aside"},
		{Name: "ls", Short: "list directory", Body: "show files"},
	}
	got := RankSkills("git commit", docs, 3)
	if len(got) == 0 || got[0].Index != 0 {
		t.Fatalf("RankSkills = %+v, want git_commit first", got)
	}
	// 名字/短描述命中应压过正文只出现一次的噪声文档。
	for _, r := range got {
		if r.Index == 1 && len(got) > 1 && got[0].Score <= r.Score {
			t.Fatalf("body-only doc outranked name doc: %+v", got)
		}
	}
}

func TestRankSkillsTopKAndEmpty(t *testing.T) {
	docs := []FieldedDoc{
		{Name: "alpha", Short: "alpha", Body: "alpha alpha"},
		{Name: "beta", Short: "beta", Body: "beta"},
		{Name: "gamma", Short: "gamma", Body: "gamma"},
	}
	got := RankSkills("alpha beta gamma", docs, 2)
	if len(got) != 2 {
		t.Fatalf("top-k = %d, want 2", len(got))
	}
	if empty := RankSkills("", docs, 3); len(empty) != 0 {
		t.Fatalf("empty query = %+v", empty)
	}
}

func TestRankSkillsCharNgramFallback(t *testing.T) {
	docs := []FieldedDoc{
		{Name: "fetch", Short: "获取网页", Body: "download a url"},
		{Name: "time", Short: "当前时间", Body: "show the clock"},
	}
	// 用不在 token 词表里的字符 bigram 强制走兜底。
	got := RankSkills("网页", docs, 2)
	if len(got) == 0 || got[0].Index != 0 {
		t.Fatalf("ngram fallback = %+v, want fetch first", got)
	}
}

func TestParseMentions(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{"dollar", "使用 $review 技能检查代码", []string{"review"}},
		{"link", "see [审查](argent://skill/code-review) for details", []string{"code-review"}},
		{"both dedup", "$review and [review](argent://skill/review)", []string{"review"}},
		{"code fence", "```sh\necho $review\n```\n$deploy", []string{"deploy"}},
		{"inline code", "use `$secret` here, then $deploy", []string{"deploy"}},
		{"path variable", "read /home/$USER/.config then $deploy", []string{"deploy"}},
		{"command placeholders", "run $ARGUMENTS with $1 and $@ but not $deploy", []string{"deploy"}},
		{"no mention", "plain text with email a$b and cost $5", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseMentions(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseMentions(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestSetTriggerContextWithPromptHybrid(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "always", Body: "baseline", Description: "always available"})
	r.Add(Skill{Name: "golang", Body: "go code", Description: "golang refactor",
		Triggers: Triggers{Paths: []string{"**/*.go"}}})
	r.Add(Skill{Name: "deploy", Body: "deploy steps", Description: "ship to production",
		Triggers: Triggers{Models: []string{"claude"}}})

	// 空路径 + 无关提示：原触发器只留 always；混合策略按 mention 加入 deploy。
	active := r.SetTriggerContextWithPrompt("", "", "gemini", "please $deploy now")
	names := skillNames(active)
	if !containsName(names, "always") || !containsName(names, "deploy") {
		t.Fatalf("hybrid active = %v, want always + deploy", names)
	}
	if containsName(names, "golang") {
		t.Fatalf("BM25/mention must not resurrect a model/path-unrestricted skill without evidence: %v", names)
	}

	// 保留原 SetTriggerContext 语义：它不受 mention 影响。
	r.ClearTriggerContext()
	plain := r.SetTriggerContext("", "", "gemini")
	if got := skillNames(plain); !reflect.DeepEqual(got, []string{"always"}) {
		t.Fatalf("SetTriggerContext = %v, want always", got)
	}
}

func TestSetTriggerContextWithPromptBM25Supplement(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "alpha", Body: "baseline", Description: "unrelated"})
	r.Add(Skill{Name: "golang", Body: "go refactor code", Description: "golang refactor",
		Triggers: Triggers{Paths: []string{"**/*.go"}}})
	// 路径命中让 golang 进入显式集；BM25 再补充相关技能。
	active := r.SetTriggerContextWithPrompt("src/main.go", "refactor", "gemini", "refactor the golang module")
	if !containsName(skillNames(active), "golang") {
		t.Fatalf("explicit trigger lost: %v", skillNames(active))
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func BenchmarkRankSkills(b *testing.B) {
	docs := []FieldedDoc{
		{Name: "git_commit", Short: "commit staged changes", Body: "run git commit"},
		{Name: "web_fetch", Short: "fetch a url", Body: "download web page"},
		{Name: "ls", Short: "list directory", Body: "show files"},
	}
	for i := 0; i < b.N; i++ {
		_ = RankSkills("git commit", docs, 3)
	}
}

func BenchmarkParseMentions(b *testing.B) {
	text := strings.Repeat("review $review [x](argent://skill/deploy) ", 16)
	for i := 0; i < b.N; i++ {
		_ = ParseMentions(text)
	}
}

// 确保 encoding/json 引用不被 gofmt 误删：占位测试用到 Skill 的 JSON 序列化稳定性。
func TestSkillEntryJSONStable(t *testing.T) {
	entry := Entry{Name: "x", Description: "y", WhenToUse: "z", Score: 99}
	raw, err := json.Marshal(entry)
	if err != nil || !strings.Contains(string(raw), `"name":"x"`) {
		t.Fatalf("entry json = %s, err = %v", raw, err)
	}
	if strings.Contains(string(raw), "score") {
		t.Fatalf("entry must not serialize Score: %s", raw)
	}
}
