package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/2118505078xyh1/argentgo/internal/server"
)

func TestSessionListJSONStableStructure(t *testing.T) {
	ts := time.Date(2026, 11, 21, 8, 30, 0, 0, time.UTC)
	metas := []sessionListMeta{
		{ID: "s1", Title: "第一条", Updated: ts, Turns: 3},
		{ID: "s2", Title: "second", Updated: ts.Add(-time.Hour), Turns: 0},
	}
	want := `[
  {
    "id": "s1",
    "title": "第一条",
    "updated": "2026-11-21T08:30:00Z",
    "turns": 3
  },
  {
    "id": "s2",
    "title": "second",
    "updated": "2026-11-21T07:30:00Z",
    "turns": 0
  }
]`
	if got := string(sessionListJSON(metas)); got != want {
		t.Fatalf("sessionListJSON() =\n%s\nwant\n%s", got, want)
	}
}

func TestSessionListJSONKeepsCJKAndHTMLCharsLiteral(t *testing.T) {
	metas := []sessionListMeta{{
		ID: "中文-id", Title: `标题 <标签> & "引号"`, Updated: time.Unix(0, 0).UTC(), Turns: 1,
	}}
	got := string(sessionListJSON(metas))
	for _, want := range []string{"中文-id", "标题 <标签> & "} {
		if !strings.Contains(got, want) {
			t.Fatalf("输出缺少 %q: %s", want, got)
		}
	}
	for _, escaped := range []string{`\u`, `\u003c`, `\u0026`} {
		if strings.Contains(got, escaped) {
			t.Fatalf("字符不应被转义为 %s: %s", escaped, got)
		}
	}
}

func TestSessionListJSONEmptyIsArray(t *testing.T) {
	for _, tc := range []struct {
		name  string
		metas []sessionListMeta
	}{
		{name: "nil"},
		{name: "empty", metas: []sessionListMeta{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(sessionListJSON(tc.metas)); got != "[]" {
				t.Fatalf("sessionListJSON() = %q, want []", got)
			}
		})
	}
}

func TestSessionListJSONFieldOrderIsDeterministic(t *testing.T) {
	metas := []sessionListMeta{{ID: "x", Title: "标题", Updated: time.Unix(0, 0).UTC(), Turns: 2}}
	first := sessionListJSON(metas)
	for i := 0; i < 5; i++ {
		if got := sessionListJSON(metas); string(got) != string(first) {
			t.Fatalf("第 %d 次输出不一致: %q vs %q", i, got, first)
		}
	}
	order := []string{`"id"`, `"title"`, `"updated"`, `"turns"`}
	last := -1
	for _, field := range order {
		at := strings.Index(string(first), field)
		if at <= last {
			t.Fatalf("字段顺序不正确 (%s): %s", field, first)
		}
		last = at
	}
}

func TestSessionTurnCountCountsTurnRecords(t *testing.T) {
	events := []server.SessionEvent{
		{Kind: "user", Data: json.RawMessage(`{"text":"hi"}`)},
		{Kind: "turn", Data: json.RawMessage(`{"turns":2}`)},
		{Kind: "tool", Data: json.RawMessage(`{}`)},
		{Kind: "turn", Data: json.RawMessage(`{"turns":0}`)},
		{Kind: "turn", Data: json.RawMessage(`not-json`)},
	}
	if got := sessionTurnCount(events); got != 4 {
		t.Fatalf("sessionTurnCount() = %d, want 4", got)
	}
}

func TestSessionsListWithoutFlagUnchanged(t *testing.T) {
	ts := time.Date(2026, 11, 21, 8, 30, 0, 0, time.UTC)
	store := &fakeStore{infos: []server.SessionInfo{
		{ID: "old", Title: "旧会话", Updated: ts.Add(-time.Hour)},
		{ID: "new", Title: "newer", Updated: ts},
	}}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"sessions", "list"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	want := "new\t2026-11-21T08:30:00Z\tnewer\nold\t2026-11-21T07:30:00Z\t旧会话\n"
	if got := out.String(); got != want {
		t.Fatalf("无 flag 输出发生变化: %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestSessionsListJSONFlagOutputsStructuredArray(t *testing.T) {
	ts := time.Date(2026, 11, 21, 8, 30, 0, 0, time.UTC)
	store := &fakeStore{
		infos: []server.SessionInfo{
			{ID: "old", Title: "旧会话", Updated: ts.Add(-time.Hour)},
			{ID: "new", Title: "新会话", Updated: ts},
		},
		events: map[string][]server.SessionEvent{
			"new": {{Kind: "turn", Data: json.RawMessage(`{"turns":3}`)}},
			"old": {{Kind: "turn", Data: json.RawMessage(`{}`)}},
		},
	}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"sessions", "list", "--json"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
	raw := out.String()
	if !strings.Contains(raw, "新会话") {
		t.Fatalf("CJK 标题被转义: %s", raw)
	}
	var rows []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Updated string `json:"updated"`
		Turns   int    `json:"turns"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("输出不是合法 JSON: %v, raw = %s", err, raw)
	}
	if len(rows) != 2 || rows[0].ID != "new" || rows[0].Title != "新会话" ||
		rows[0].Updated != "2026-11-21T08:30:00Z" || rows[0].Turns != 3 {
		t.Fatalf("rows[0] = %+v", rows[0])
	}
	if rows[1].ID != "old" || rows[1].Turns != 1 {
		t.Fatalf("rows[1] = %+v", rows[1])
	}
}

func TestSessionsListJSONFlagEmptyList(t *testing.T) {
	ioState, out, _ := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: &fakeStore{}}, []string{"sessions", "list", "--json"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if got := strings.TrimSpace(out.String()); got != "[]" {
		t.Fatalf("out = %q, want []", got)
	}
}

func TestSessionsListJSONFlagLoadFailureKeepsStdoutClean(t *testing.T) {
	store := &fakeStore{infos: []server.SessionInfo{{ID: "lost", Title: "缺失"}}}
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: store}, []string{"sessions", "list", "--json"}); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	var rows []sessionListMeta
	if err := json.Unmarshal([]byte(out.String()), &rows); err != nil || len(rows) != 1 || rows[0].Turns != 0 {
		t.Fatalf("stdout 不是预期的 JSON: %v, raw = %q", err, out.String())
	}
	if !strings.Contains(errOut.String(), "lost") {
		t.Fatalf("缺少加载失败提示: %q", errOut.String())
	}
}

func TestSessionsListJSONFlagRejectsUnknownArgument(t *testing.T) {
	ioState, out, errOut := newTestIO("", nil)
	if code := RunWith(App{IO: ioState, Sessions: &fakeStore{}}, []string{"sessions", "list", "--nope"}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "--nope") {
		t.Fatalf("out = %q, err = %q", out.String(), errOut.String())
	}
}
