package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
)

type fakeUserAsker struct {
	calls     int
	questions []pluginapi.UserQuestion
	answers   []pluginapi.UserAnswer
	err       error
}

func (f *fakeUserAsker) AskUser(_ context.Context, qs []pluginapi.UserQuestion) ([]pluginapi.UserAnswer, error) {
	f.calls++
	f.questions = qs
	return f.answers, f.err
}

// TestRegisterAskUserKeepsDefaultGolden 守护核心承诺：ask_user 不进默认注册表，
// 显式注册幂等且只追加到末尾，默认 Specs()/Names() 逐字节不变。
func TestRegisterAskUserKeepsDefaultGolden(t *testing.T) {
	baseline := New(t.TempDir(), true).Names()
	r := New(t.TempDir(), true)
	want := append(append([]string(nil), baseline...), askUserName)
	RegisterAskUser(r, &fakeUserAsker{})
	if got := r.Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	// 幂等：重复注册不再追加。
	RegisterAskUser(r, &fakeUserAsker{})
	if len(r.Names()) != len(want) {
		t.Fatalf("重复注册改变了清单: %v", r.Names())
	}
	// 默认注册表未受影响。
	if got := New(t.TempDir(), true).Names(); !reflect.DeepEqual(got, baseline) {
		t.Fatalf("默认注册表被污染: %v", got)
	}
	RegisterAskUser(nil, nil) // nil 安全
}

func TestValidateAskUserQuestions(t *testing.T) {
	valid := []pluginapi.UserQuestion{{ID: "q1", Prompt: "继续？", Kind: pluginapi.UserQuestionKindSingle, Options: []string{"是", "否"}}}
	cases := []struct {
		name    string
		qs      []pluginapi.UserQuestion
		wantErr string
	}{
		{"valid", valid, ""},
		{"empty", nil, "non-empty"},
		{"empty id", []pluginapi.UserQuestion{{Prompt: "p", Kind: "text"}}, "empty id"},
		{"duplicate id", []pluginapi.UserQuestion{{ID: "x", Prompt: "p", Kind: "text"}, {ID: "x", Prompt: "q", Kind: "text"}}, "duplicate"},
		{"empty prompt", []pluginapi.UserQuestion{{ID: "x", Kind: "text"}}, "empty prompt"},
		{"bad kind", []pluginapi.UserQuestion{{ID: "x", Prompt: "p", Kind: "weird"}}, "invalid kind"},
		{"single without options", []pluginapi.UserQuestion{{ID: "x", Prompt: "p", Kind: "single"}}, "at least one option"},
		{"duplicate option", []pluginapi.UserQuestion{{ID: "x", Prompt: "p", Kind: "single", Options: []string{"a", "a"}}}, "duplicate option"},
		{"too many questions", func() []pluginapi.UserQuestion {
			out := make([]pluginapi.UserQuestion, askUserMaxQuestions+1)
			for i := range out {
				out[i] = pluginapi.UserQuestion{ID: string(rune('a' + i)), Prompt: "p", Kind: "text"}
			}
			return out
		}(), "too many questions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateAskUserQuestions(tc.qs)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestAskUserToolRun(t *testing.T) {
	asker := &fakeUserAsker{answers: []pluginapi.UserAnswer{{ID: "q1", Value: "是"}}}
	tool := askUserTool{asker: asker}
	raw := json.RawMessage(`{"questions":[{"id":"q1","prompt":"继续？","kind":"single","options":["是","否"]}]}`)
	got, err := tool.Run(context.Background(), t.TempDir(), raw)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(got, `"value":"是"`) || asker.calls != 1 {
		t.Fatalf("result = %q, calls = %d", got, asker.calls)
	}
	if len(asker.questions) != 1 || asker.questions[0].ID != "q1" {
		t.Fatalf("asker got %+v", asker.questions)
	}

	// 无交互通道时报错而非静默成功。
	if _, err := (askUserTool{}).Run(context.Background(), t.TempDir(), raw); err == nil {
		t.Fatal("nil asker should error")
	}

	// 答案 ID 不匹配时报错。
	asker2 := &fakeUserAsker{answers: []pluginapi.UserAnswer{{ID: "wrong", Value: "x"}}}
	if _, err := (askUserTool{asker: asker2}).Run(context.Background(), t.TempDir(), raw); err == nil {
		t.Fatal("mismatched answer id should error")
	}
}
