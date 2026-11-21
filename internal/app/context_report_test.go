package app

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/2118505078xyh1/argentgo/internal/agent"
	"github.com/2118505078xyh1/argentgo/internal/cli"
	"github.com/2118505078xyh1/argentgo/internal/session"
	"github.com/2118505078xyh1/argentgo/internal/tools"
)

// contextReportEnv 把存储目录钉在临时目录里，避免测试读取真实 $HOME 下的会话或配置。
func contextReportEnv(t *testing.T, storage string) func(string) string {
	t.Helper()
	return envFrom(map[string]string{
		"HOME":           t.TempDir(),
		"USERPROFILE":    t.TempDir(),
		"ARGENT_STORAGE": storage,
	})
}

// 系统提示与工具 schema 必须与 TokenReport 完全同口径：两者都走 Resolve、
// tools.NewWithLimit(...).Specs() 以及 agent 的同一组估算函数。
func TestContextReportMatchesTokenReportOverhead(t *testing.T) {
	root := t.TempDir()
	storage := t.TempDir()
	env := contextReportEnv(t, storage)
	opts := cli.Options{Cwd: root}
	before := opts

	res, err := Resolve(cli.IO{Env: env}, opts)
	if err != nil {
		t.Fatal(err)
	}
	specs := tools.NewWithLimit(res.Root, res.Mutating, 0).Specs()
	wantSystem := agent.EstimateTokens(res.System)
	wantSchema := agent.EstimateTools(specs)

	tokensOut, err := TokenReport(cli.IO{Env: env}, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf("system prompt: ~%d tokens", wantSystem),
		fmt.Sprintf("tools: %d specs, ~%d tokens", len(specs), wantSchema),
	} {
		if !strings.Contains(tokensOut, want) {
			t.Errorf("TokenReport() missing %q:\n%s", want, tokensOut)
		}
	}

	out, err := ContextReport(cli.IO{Env: env}, opts)
	if err != nil {
		t.Fatal(err)
	}
	// 没有会话时历史和文件都是 0；整段分解交给 cli 的原语生成，逐字节比对。
	wantBreakdown := cli.FormatContextBreakdown(cli.BreakdownContext(wantSystem, wantSchema, 0, 0))
	if !strings.Contains(out, wantBreakdown) {
		t.Errorf("ContextReport() missing breakdown:\n%s\nwant:\n%s", out, wantBreakdown)
	}
	if !strings.Contains(out, "历史消息") || !strings.Contains(out, "文件内容") {
		t.Errorf("ContextReport() missing categories:\n%s", out)
	}
	if !strings.Contains(out, contextFilesNote) {
		t.Errorf("ContextReport() missing files note:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("ContextReport() does not end with a newline: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("ContextReport() contains invalid UTF-8: %q", out)
	}
	if !reflect.DeepEqual(opts, before) {
		t.Errorf("ContextReport() mutated opts: got %+v, want %+v", opts, before)
	}
}

// 上下文窗口未知时，健康行必须是 cli.ContextHealth 约定的固定文案，
// 且不会把未知模型名以外的原文内容带进输出。CJK 模型名保持完整 UTF-8。
func TestContextReportUnknownWindowShowsFixedHealthText(t *testing.T) {
	root := t.TempDir()
	env := contextReportEnv(t, t.TempDir())
	model := "未知模型-测试甲"

	out, err := ContextReport(cli.IO{Env: env}, cli.Options{Cwd: root, Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "model: "+model) {
		t.Errorf("ContextReport() lost CJK model name:\n%s", out)
	}
	if !strings.Contains(out, "上下文窗口未知，无法评估占用") {
		t.Errorf("ContextReport() missing fixed health text:\n%s", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("ContextReport() contains invalid UTF-8: %q", out)
	}
}

func newContextReportSession(t *testing.T, storage, userText string) *session.Session {
	t.Helper()
	store, err := session.Open(filepath.Join(storage, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.NewSession("上下文报告")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Append(session.KindUser, userText); err != nil {
		t.Fatal(err)
	}
	// tool 角色必须被排除，否则历史占用会被凭空放大。
	if err := sess.Append(session.KindTool, "这段工具输出不应被计入"); err != nil {
		t.Fatal(err)
	}
	return sess
}

// 历史 token 只来自持久化会话的非 tool 消息：无 --session 取最新会话，
// 显式 --session 也走同一条恢复路径，两者应得到完全相同的分解。
func TestContextReportUsesPersistedSessionHistory(t *testing.T) {
	root := t.TempDir()
	storage := t.TempDir()
	env := contextReportEnv(t, storage)
	userText := "这是一段用于估算历史占用的中文消息，包含全角标点：你好，世界！"
	sess := newContextReportSession(t, storage, userText)

	wantHistory := agent.EstimateTokens(userText)
	res, err := Resolve(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	specs := tools.NewWithLimit(res.Root, res.Mutating, 0).Specs()
	wantBreakdown := cli.FormatContextBreakdown(cli.BreakdownContext(
		agent.EstimateTokens(res.System), agent.EstimateTools(specs), wantHistory, 0,
	))

	latest, err := ContextReport(cli.IO{Env: env}, cli.Options{Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(latest, wantBreakdown) {
		t.Errorf("ContextReport() without --session missing session history:\n%s\nwant:\n%s", latest, wantBreakdown)
	}
	if !strings.Contains(latest, contextFilesNote) {
		t.Errorf("ContextReport() with session missing files note:\n%s", latest)
	}

	addressed, err := ContextReport(cli.IO{Env: env}, cli.Options{Cwd: root, Session: sess.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(addressed, wantBreakdown) {
		t.Errorf("ContextReport() with --session missing session history:\n%s\nwant:\n%s", addressed, wantBreakdown)
	}
}
