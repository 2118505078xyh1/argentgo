package app

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/pluginapi"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

func TestReadAskLineDoesNotPread(t *testing.T) {
	// 逐字节读：只消费到换行符，后续字节仍留在 reader 中供审批器读取。
	r := strings.NewReader("answer\nNEXT")
	line, err := readAskLine(r)
	if err != nil || line != "answer" {
		t.Fatalf("line=%q err=%v", line, err)
	}
	rest, err := io.ReadAll(r)
	if err != nil || string(rest) != "NEXT" {
		t.Fatalf("rest=%q err=%v — 预读偷走了后续输入", rest, err)
	}
	// 无换行的末行也能读回。
	line, err = readAskLine(strings.NewReader("tail"))
	if err != nil || line != "tail" {
		t.Fatalf("tail line=%q err=%v", line, err)
	}
}

func TestResolveAskOption(t *testing.T) {
	opts := []string{"是", "否", "稍后"}
	cases := []struct{ in, want string }{
		{"1", "是"}, {"3", "稍后"}, {"否", "否"}, {"9", "9"}, {"", ""},
	}
	for _, tc := range cases {
		if got := resolveAskOption(opts, tc.in); got != tc.want {
			t.Errorf("resolveAskOption(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTextUserAskerAllKinds(t *testing.T) {
	in := strings.NewReader("自由回答\n2\n1,3\n")
	var out bytes.Buffer
	asker := &textUserAsker{in: in, out: &out}
	qs := []pluginapi.UserQuestion{
		{ID: "q1", Prompt: "说说想法", Kind: pluginapi.UserQuestionKindText},
		{ID: "q2", Prompt: "选一个", Kind: pluginapi.UserQuestionKindSingle, Options: []string{"甲", "乙"}},
		{ID: "q3", Prompt: "选多个", Kind: pluginapi.UserQuestionKindMultiple, Options: []string{"A", "B", "C"}},
	}
	answers, err := asker.AskUser(context.Background(), qs)
	if err != nil {
		t.Fatalf("AskUser: %v", err)
	}
	if len(answers) != 3 {
		t.Fatalf("answers = %+v", answers)
	}
	if answers[0].Value != "自由回答" {
		t.Errorf("text answer = %q", answers[0].Value)
	}
	if answers[1].Value != "乙" {
		t.Errorf("single answer = %q", answers[1].Value)
	}
	if len(answers[2].Values) != 2 || answers[2].Values[0] != "A" || answers[2].Values[1] != "C" {
		t.Errorf("multiple answer = %+v", answers[2].Values)
	}
	// 提示确实写到了输出流。
	if !strings.Contains(out.String(), "[ask_user] 说说想法") || !strings.Contains(out.String(), "1) 甲") {
		t.Errorf("prompt output = %q", out.String())
	}
}

func TestTextUserAskerEOF(t *testing.T) {
	asker := &textUserAsker{in: strings.NewReader(""), out: &bytes.Buffer{}}
	_, err := asker.AskUser(context.Background(), []pluginapi.UserQuestion{{ID: "q", Prompt: "p", Kind: "text"}})
	if err == nil {
		t.Fatal("EOF with no input should error")
	}
}

func TestWireAskUserTTYOnly(t *testing.T) {
	r := tools.New(t.TempDir(), true)
	set := &toolset{base: &multiRootTools{regs: []*tools.Registry{r}}}

	// 非 TTY：不注册，工具列表不变。
	wireAskUser(cli.IO{In: strings.NewReader("y\n"), Out: &bytes.Buffer{}}, set)
	for _, name := range r.Names() {
		if name == "ask_user" {
			t.Fatal("ask_user must not be registered without a terminal")
		}
	}

	// TTY：注册且只注册一次。
	ttyIO := cli.IO{
		In:          strings.NewReader("x\n"),
		Out:         &bytes.Buffer{},
		HasTerminal: func() bool { return true },
	}
	wireAskUser(ttyIO, set)
	wireAskUser(ttyIO, set) // 幂等
	count := 0
	for _, name := range r.Names() {
		if name == "ask_user" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ask_user registered %d times, want 1", count)
	}
}
