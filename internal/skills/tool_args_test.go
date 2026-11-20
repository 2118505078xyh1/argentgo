package skills

import (
	"encoding/json"
	"strings"
	"testing"
)

const useSkillDemoPrefix = "# skill: demo\n(source: /tmp/demo/SKILL.md)\n\n"

// registryWithDemoBody 构造只含一个技能且正文可控的注册表，避免测试依赖磁盘文件。
func registryWithDemoBody(body string) *Registry {
	r := New()
	r.Add(Skill{Name: "demo", Description: "demo skill", Path: "/tmp/demo/SKILL.md", Body: body})
	return r
}

// useSkillRendered 调用 use_skill 并剥掉固定头部，返回渲染后的正文。
func useSkillRendered(t *testing.T, r *Registry, rawArgs string) string {
	t.Helper()
	out, err := r.HandleUseSkill(json.RawMessage(rawArgs))
	if err != nil {
		t.Fatalf("HandleUseSkill(%s): %v", rawArgs, err)
	}
	if !strings.HasPrefix(out, useSkillDemoPrefix) {
		t.Fatalf("output %q missing header %q", out, useSkillDemoPrefix)
	}
	return strings.TrimPrefix(out, useSkillDemoPrefix)
}

// TestHandleUseSkillWithoutArgumentsKeepsBody 锁定最低风险的默认行为：
// 未提供参数（或为空串）时正文逐字节返回，占位符保持原义。
func TestHandleUseSkillWithoutArgumentsKeepsBody(t *testing.T) {
	body := "Keep $1, $ARGUMENTS, $@ and ${2} exactly.\n第二行  \n"
	r := registryWithDemoBody(body)

	for _, rawArgs := range []string{
		`{"name":"demo"}`,
		`{"name":"demo","arguments":""}`,
	} {
		out, err := r.HandleUseSkill(json.RawMessage(rawArgs))
		if err != nil {
			t.Fatalf("HandleUseSkill(%s): %v", rawArgs, err)
		}
		if want := useSkillDemoPrefix + body; out != want {
			t.Errorf("HandleUseSkill(%s) = %q, want byte-for-byte %q", rawArgs, out, want)
		}
	}
}

// TestHandleUseSkillArgumentsPositional 验证按空白拆分的位置参数替换。
func TestHandleUseSkillArgumentsPositional(t *testing.T) {
	r := registryWithDemoBody("$1 / $2 / $3")
	got := useSkillRendered(t, r, `{"name":"demo","arguments":"alpha beta gamma"}`)
	if want := "alpha / beta / gamma"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestHandleUseSkillArgumentsNamed 验证 $ARGUMENTS 与 $@ 展开为完整参数串。
func TestHandleUseSkillArgumentsNamed(t *testing.T) {
	r := registryWithDemoBody("all=$ARGUMENTS at=$@")
	got := useSkillRendered(t, r, `{"name":"demo","arguments":"alpha beta"}`)
	if want := "all=alpha beta at=alpha beta"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestHandleUseSkillArgumentsBraced 验证 ${N} 形式在相邻文本中也能展开。
func TestHandleUseSkillArgumentsBraced(t *testing.T) {
	r := registryWithDemoBody("pre${1}post and ${2}")
	got := useSkillRendered(t, r, `{"name":"demo","arguments":"alpha beta"}`)
	if want := "prealphapost and beta"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestHandleUseSkillArgumentsCJKAndQuotes 验证带引号的参数算作一个位置且 CJK 正常保留。
func TestHandleUseSkillArgumentsCJKAndQuotes(t *testing.T) {
	r := registryWithDemoBody("目标=$1 动作=$2")
	got := useSkillRendered(t, r, `{"name":"demo","arguments":"'中文 参数' 部署"}`)
	if want := "目标=中文 参数 动作=部署"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestHandleUseSkillArgumentsOutOfRange 验证越界的位置参数被替换为空。
func TestHandleUseSkillArgumentsOutOfRange(t *testing.T) {
	r := registryWithDemoBody("x$1|$3|$9|y")
	got := useSkillRendered(t, r, `{"name":"demo","arguments":"only"}`)
	if want := "xonly|||y"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestUseSkillSpecHasArgumentsProperty 确认工具规格向模型公开了 arguments 参数。
func TestUseSkillSpecHasArgumentsProperty(t *testing.T) {
	spec := UseSkillSpec()
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Function.Parameters, &schema); err != nil {
		t.Fatalf("parameters: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("schema type = %q", schema.Type)
	}
	arg, ok := schema.Properties["arguments"]
	if !ok {
		t.Fatalf("arguments property missing: %#v", schema.Properties)
	}
	if arg.Type != "string" {
		t.Errorf("arguments type = %q, want string", arg.Type)
	}
}
