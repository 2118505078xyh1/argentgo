package app

import (
	"testing"

	"example.com/argentgo/internal/agent"
	"example.com/argentgo/internal/session"
)

func newRouterTestSession(t *testing.T) *session.Session {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("vm")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

type fakeModelSetter struct{ model string }

func (f *fakeModelSetter) SetModel(model string) { f.model = model }

func TestClassifyTask(t *testing.T) {
	cases := []struct {
		name     string
		decision PromptDecision
		prompt   string
		code     bool
		mutating bool
		want     TaskClass
	}{
		{"simple", PromptDecision{}, "你好", false, false, TaskSimple},
		{"code is standard", PromptDecision{}, "写个函数", true, false, TaskStandard},
		{"mutating is standard", PromptDecision{}, "修改文件", false, true, TaskStandard},
		{"judge wins", PromptDecision{UseSecondary: true}, "你好", false, false, TaskComplex},
		{"long is complex", PromptDecision{}, string(make([]rune, 2100)), false, false, TaskComplex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyTask(tc.decision, tc.prompt, tc.code, tc.mutating); got != tc.want {
				t.Fatalf("class = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVirtualModelRouting(t *testing.T) {
	raw := map[string]any{
		"virtualModels": map[string]any{
			"auto": map[string]any{"simple": "cheap", "standard": "main", "complex": "strong"},
		},
	}
	routes := virtualModelsFrom(raw["virtualModels"])
	if len(routes) != 1 {
		t.Fatalf("routes = %+v", routes)
	}
	if model, ok := ResolveVirtualModel(routes, "auto", TaskSimple, "main"); !ok || model != "cheap" {
		t.Fatalf("simple route = %q/%v", model, ok)
	}
	if _, ok := ResolveVirtualModel(routes, "auto", TaskStandard, "main"); ok {
		t.Fatal("routing to current model must be a no-op")
	}
	if _, ok := ResolveVirtualModel(routes, "missing", TaskSimple, "main"); ok {
		t.Fatal("unknown virtual model must be a no-op")
	}
}

func TestApplyVirtualModelRoutePersistsDecision(t *testing.T) {
	sess := newRouterTestSession(t)
	setter := &fakeModelSetter{}
	raw := map[string]any{
		"auto": map[string]any{"simple": "cheap", "complex": "strong"},
	}
	opts := &agent.Options{Model: "main"}

	applyVirtualModelRoute(opts, setter, raw, PromptDecision{}, "你好", false, false, sess, nil)
	if setter.model != "cheap" {
		t.Fatalf("setter model = %q, want cheap", setter.model)
	}
	routes, err := sess.ModelRoutes()
	if err != nil || len(routes) != 1 || routes[0].Model != "cheap" || routes[0].Task != "simple" {
		t.Fatalf("routes = %+v / %v", routes, err)
	}

	// judge 建议复杂任务：路由到 strong，且同 virtual+task 的最后一次决策胜出。
	applyVirtualModelRoute(opts, setter, raw, PromptDecision{UseSecondary: true}, "你好", false, false, sess, nil)
	if setter.model != "strong" {
		t.Fatalf("setter model = %q, want strong", setter.model)
	}
	routes, err = sess.ModelRoutes()
	if err != nil || len(routes) != 2 || routes[1].Model != "strong" {
		t.Fatalf("routes = %+v / %v", routes, err)
	}

	// 无配置 / 无 setter：零影响。
	other := &fakeModelSetter{}
	applyVirtualModelRoute(opts, other, nil, PromptDecision{}, "你好", false, false, sess, nil)
	if other.model != "" {
		t.Fatalf("no-config path changed model: %q", other.model)
	}
}
