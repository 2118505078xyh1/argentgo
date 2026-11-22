package skills

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// 本文件守护"触发器真正限制 use_skill 可见性"这条契约：Registry 解析出的
// triggers 不再是纯元数据，收紧后只有 active 子集可按名称加载。无触发器的
// 技能始终可见，因此内置/无触发器技能逐字节不变。所有用例不联网、不读真实模型。

// gateRegistry 构造四个技能：一个无触发器基线，三个分别限制 paths / keywords / models。
func gateRegistry() *Registry {
	r := New()
	r.Add(Skill{Name: "always", Body: "baseline body", Description: "always available"})
	r.Add(Skill{Name: "golang", Body: "go body", Description: "go only",
		Triggers: Triggers{Paths: []string{"**/*.go"}}})
	r.Add(Skill{Name: "refactor", Body: "refactor body", Description: "refactor only",
		Triggers: Triggers{Keywords: []string{"重构", "refactor"}}})
	r.Add(Skill{Name: "claude-only", Body: "claude body", Description: "claude only",
		Triggers: Triggers{Models: []string{"claude"}}})
	return r
}

// assertLoadable 断言 use_skill 按名称加载 name 是否成功。失败必须是 ErrNotFound，
// 而不是别的错误，这样测试用例才能区分"被触发器排除"与"正文展开出错"。
func assertLoadable(t *testing.T, r *Registry, name string, want bool) {
	t.Helper()
	body, err := r.HandleUseSkill(json.RawMessage(`{"name":"` + name + `"}`))
	if want {
		if err != nil || !strings.Contains(body, "# skill: "+name) {
			t.Errorf("load %q: want success, got body=%q err=%v", name, body, err)
		}
		return
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("load %q: want ErrNotFound, got body=%q err=%v", name, body, err)
	}
}

// TestUseSkillRestrictedByTriggers 分别用 paths / keywords / models 三个维度的
// 上下文收紧可见集，断言只有匹配（或无触发器）的技能能被 use_skill 加载。
func TestUseSkillRestrictedByTriggers(t *testing.T) {
	cases := []struct {
		name        string
		path        string
		keywords    string
		modelFamily string
		want        map[string]bool
	}{
		{
			name: "paths only",
			path: "src/main.go",
			want: map[string]bool{"always": true, "golang": true, "refactor": false, "claude-only": false},
		},
		{
			name:     "keywords only (CJK)",
			keywords: "请重构这个模块",
			want:     map[string]bool{"always": true, "golang": false, "refactor": true, "claude-only": false},
		},
		{
			name:        "models only",
			modelFamily: "CLAUDE",
			want:        map[string]bool{"always": true, "golang": false, "refactor": false, "claude-only": true},
		},
		{
			name:        "intersection: wrong path and wrong model drop both",
			path:        "README.md",
			keywords:    "请重构",
			modelFamily: "gemini",
			want:        map[string]bool{"always": true, "golang": false, "refactor": true, "claude-only": false},
		},
		{
			name: "empty context keeps only unrestricted",
			want: map[string]bool{"always": true, "golang": false, "refactor": false, "claude-only": false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gateRegistry()
			active := r.SetTriggerContext(tc.path, tc.keywords, tc.modelFamily)
			for name, want := range tc.want {
				assertLoadable(t, r, name, want)
			}
			var activeNames []string
			for _, s := range active {
				activeNames = append(activeNames, s.Name)
			}
			for name, want := range tc.want {
				if got := containsString(activeNames, name); got != want {
					t.Errorf("active set contains %q = %v, want %v (active=%v)", name, got, want, activeNames)
				}
			}
		})
	}
}

// TestTriggerGateNoTriggersByteStable 验证没有触发器时收紧是无操作：
// 无论上下文如何，全部技能可见，列举输出与按名加载都和收紧前一致。
func TestTriggerGateNoTriggersByteStable(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "alpha", Body: "body alpha", Description: "a"})
	r.Add(Skill{Name: "beta", Body: "body beta", Description: "b"})

	beforeList, err := r.HandleUseSkill(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	beforeIndex := r.IndexText()

	active := r.SetTriggerContext("anything/at/all.go", "任意提示", "gemini")
	if got := skillNames(active); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("active = %v, want all skills", got)
	}
	if got := skillNames(r.ActiveSkills()); !reflect.DeepEqual(got, []string{"alpha", "beta"}) {
		t.Fatalf("ActiveSkills = %v", got)
	}
	afterList, err := r.HandleUseSkill(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if afterList != beforeList {
		t.Errorf("list changed after gate: %q -> %q", beforeList, afterList)
	}
	if r.IndexText() != beforeIndex {
		t.Errorf("system prompt index changed after gate")
	}
	assertLoadable(t, r, "alpha", true)
	assertLoadable(t, r, "beta", true)

	// 撤销收紧后行为保持一致。
	r.ClearTriggerContext()
	assertLoadable(t, r, "alpha", true)
}

// TestTriggerGateCJK 覆盖 CJK 关键词：MatchKeywords 用子串匹配，CJK 不应被按词切分丢弃。
func TestTriggerGateCJK(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "基线", Body: "基线正文"})
	r.Add(Skill{Name: "重构流程", Body: "重构正文", Description: "处理重构",
		Triggers: Triggers{Keywords: []string{"重构"}}})

	r.SetTriggerContext("", "请重构这里", "")
	assertLoadable(t, r, "重构流程", true)
	assertLoadable(t, r, "基线", true)

	r.SetTriggerContext("", "无关请求", "")
	assertLoadable(t, r, "重构流程", false)
	assertLoadable(t, r, "基线", true)
}

// TestIndexTextForMatchesIndexText 断言 IndexTextFor 与 IndexText 共用同一条渲染链：
// 对 active 子集的渲染，逐字节等于"只安装这些技能"的 registry 的 IndexText。
func TestIndexTextForMatchesIndexText(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "alpha", Description: "first", WhenToUse: "when alpha"})
	r.Add(Skill{Name: "beta", Description: "第二行\n含 CJK", WhenToUse: "when beta"})
	r.Add(Skill{Name: "gamma", Description: "third"})

	subset := []Skill{r.All()[1], r.All()[2]}
	only := New()
	for _, s := range subset {
		only.Add(s)
	}
	if got, want := IndexTextFor(subset), only.IndexText(); got != want {
		t.Errorf("IndexTextFor(subset) = %q, want %q", got, want)
	}
	// 全量渲染仍与 Registry.IndexText 逐字节一致。
	if got, want := IndexTextFor(r.All()), r.IndexText(); got != want {
		t.Errorf("IndexTextFor(all) = %q, want %q", got, want)
	}
	if got := IndexTextFor(nil); got != "" {
		t.Errorf("IndexTextFor(nil) = %q, want empty", got)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
