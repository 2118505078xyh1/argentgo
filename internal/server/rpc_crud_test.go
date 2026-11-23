package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crudSessions 同时实现 SessionStore 与三个可选 CRUD 写入端。
// 每次调用记录参数，错误由字段注入，便于表驱动断言。
type crudSessions struct {
	createErr error
	renameErr error
	deleteErr error
	nextID    string

	createdTitle string
	renamedID    string
	renamedTitle string
	deletedID    string
}

func (f *crudSessions) List() ([]SessionInfo, error)        { return nil, nil }
func (f *crudSessions) Load(string) ([]SessionEvent, error) { return nil, nil }

func (f *crudSessions) NewSession(title string) (string, error) {
	f.createdTitle = title
	if f.createErr != nil {
		return "", f.createErr
	}
	if f.nextID != "" {
		return f.nextID, nil
	}
	return "new-123", nil
}

func (f *crudSessions) RenameSession(id, title string) error {
	f.renamedID, f.renamedTitle = id, title
	return f.renameErr
}

func (f *crudSessions) Delete(id string) error {
	f.deletedID = id
	return f.deleteErr
}

// sessionIDRunner 暴露当前会话 id，用于验证删除活动会话的守卫。
type sessionIDRunner struct {
	id string
}

func (s *sessionIDRunner) Prompt(_ context.Context, _ string, _ func(string)) (string, error) {
	return "", nil
}
func (s *sessionIDRunner) Abort()            {}
func (s *sessionIDRunner) SessionID() string { return s.id }

// crudRPCResponse 是 CRUD 方法回复的最小解码形态。
type crudRPCResponse struct {
	Result map[string]any `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// crudRequest 构造一条单个 CRUD 方法的 JSON-RPC 请求；params 为空时省略该字段。
func crudRequest(method, params string) string {
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q`, method)
	if params != "" {
		req += `,"params":` + params
	}
	return req + `}`
}

// runCRUDRequest 跑单个 RPC 请求并解码其唯一回复帧。
func runCRUDRequest(t *testing.T, cfg Config, request string) crudRPCResponse {
	t.Helper()
	lines := runRPCBuffer(t, cfg, request)
	if len(lines) != 1 {
		t.Fatalf("got %d frames (%q), want 1", len(lines), lines)
	}
	var resp crudRPCResponse
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("decode response %q: %v", lines[0], err)
	}
	return resp
}

// assertCRUDError 校验错误码与消息里必须保留/必须消失的片段。
func assertCRUDError(t *testing.T, resp crudRPCResponse, code int, wantKeep, wantAbsent []string) {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != code {
		t.Fatalf("error = %+v, want code %d", resp.Error, code)
	}
	for _, keep := range wantKeep {
		if !strings.Contains(resp.Error.Message, keep) {
			t.Fatalf("message %q lost %q", resp.Error.Message, keep)
		}
	}
	for _, leak := range wantAbsent {
		if strings.Contains(resp.Error.Message, leak) {
			t.Fatalf("message %q leaked %q", resp.Error.Message, leak)
		}
	}
}

// TestRPCSessionNew 覆盖 session.new 的成功、非法参数、未配置与错误脱敏。
func TestRPCSessionNew(t *testing.T) {
	storage := filepath.Join(os.TempDir(), "argentgo", "sessions", "leak.jsonl")
	cases := []struct {
		name       string
		store      SessionStore
		params     string
		wantID     string
		wantTitle  string
		wantCode   int
		wantKeep   []string
		wantAbsent []string
	}{
		{
			name:      "success with title",
			store:     &crudSessions{nextID: "new-1"},
			params:    `{"title":"我的会话"}`,
			wantID:    "new-1",
			wantTitle: "我的会话",
		},
		{
			name:      "success without title uses default",
			store:     &crudSessions{nextID: "new-2"},
			params:    `{}`,
			wantID:    "new-2",
			wantTitle: "session",
		},
		{
			name:      "success with omitted params",
			store:     &crudSessions{nextID: "new-3"},
			wantID:    "new-3",
			wantTitle: "session",
		},
		{
			name:     "invalid params",
			store:    &crudSessions{},
			params:   `{"title":123}`,
			wantCode: codeInvalidParams,
		},
		{
			name:     "not configured",
			store:    fakeSessions{},
			params:   `{}`,
			wantCode: codeServerError,
			wantKeep: []string{"session creation not configured"},
		},
		{
			name:     "no store",
			params:   `{}`,
			wantCode: codeServerError,
			wantKeep: []string{"session store not configured"},
		},
		{
			name:       "store error scrubbed",
			store:      &crudSessions{createErr: fmt.Errorf("session: create %s: denied with sk-secret123456789", storage)},
			params:     `{"title":"x"}`,
			wantCode:   codeServerError,
			wantKeep:   []string{"<storage>/...", "denied"},
			wantAbsent: []string{storage, filepath.ToSlash(storage), "sk-secret123456789"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := runCRUDRequest(t, Config{Runner: &fakeRunner{}, Sessions: tc.store}, crudRequest("session.new", tc.params))
			if tc.wantCode != 0 {
				assertCRUDError(t, resp, tc.wantCode, tc.wantKeep, tc.wantAbsent)
				return
			}
			if resp.Error != nil {
				t.Fatalf("unexpected error: %+v", resp.Error)
			}
			if resp.Result["id"] != tc.wantID {
				t.Fatalf("result = %v, want id %q", resp.Result, tc.wantID)
			}
			store, ok := tc.store.(*crudSessions)
			if !ok {
				t.Fatalf("store = %T, want *crudSessions", tc.store)
			}
			if store.createdTitle != tc.wantTitle {
				t.Fatalf("created title = %q, want %q", store.createdTitle, tc.wantTitle)
			}
		})
	}
}

// TestRPCSessionRename 覆盖 session.rename 的成功（含清空覆盖）、非法 id、
// 缺失 title、不存在与错误脱敏。
func TestRPCSessionRename(t *testing.T) {
	storage := filepath.Join(os.TempDir(), "argentgo", "sessions", "gone.jsonl")
	cases := []struct {
		name       string
		store      SessionStore
		params     string
		wantID     string
		wantTitle  string
		wantCode   int
		wantKeep   []string
		wantAbsent []string
	}{
		{
			name:      "success",
			store:     &crudSessions{},
			params:    `{"id":"s1","title":"新标题"}`,
			wantID:    "s1",
			wantTitle: "新标题",
		},
		{
			name:      "explicit empty title clears override",
			store:     &crudSessions{},
			params:    `{"id":"s1","title":""}`,
			wantID:    "s1",
			wantTitle: "",
		},
		{
			name:     "missing id",
			store:    &crudSessions{},
			params:   `{"title":"x"}`,
			wantCode: codeInvalidParams,
			wantKeep: []string{"session id is required"},
		},
		{
			name:     "blank id",
			store:    &crudSessions{},
			params:   `{"id":"   ","title":"x"}`,
			wantCode: codeInvalidParams,
			wantKeep: []string{"session id is required"},
		},
		{
			name:     "missing title",
			store:    &crudSessions{},
			params:   `{"id":"s1"}`,
			wantCode: codeInvalidParams,
			wantKeep: []string{"title is required"},
		},
		{
			name:     "invalid params",
			store:    &crudSessions{},
			params:   `{"id":123,"title":"x"}`,
			wantCode: codeInvalidParams,
		},
		{
			name:     "not configured",
			store:    fakeSessions{},
			params:   `{"id":"s1","title":"x"}`,
			wantCode: codeServerError,
			wantKeep: []string{"session rename not configured"},
		},
		{
			name:     "no store",
			params:   `{"id":"s1","title":"x"}`,
			wantCode: codeServerError,
			wantKeep: []string{"session store not configured"},
		},
		{
			name:       "not found error scrubbed",
			store:      &crudSessions{renameErr: fmt.Errorf("session: read %s: no session with id %q", storage, "missing")},
			params:     `{"id":"missing","title":"x"}`,
			wantCode:   codeServerError,
			wantKeep:   []string{"rename session", "missing", "<storage>/..."},
			wantAbsent: []string{storage, filepath.ToSlash(storage)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := runCRUDRequest(t, Config{Runner: &fakeRunner{}, Sessions: tc.store}, crudRequest("session.rename", tc.params))
			if tc.wantCode != 0 {
				assertCRUDError(t, resp, tc.wantCode, tc.wantKeep, tc.wantAbsent)
				return
			}
			if resp.Error != nil {
				t.Fatalf("unexpected error: %+v", resp.Error)
			}
			if resp.Result["id"] != tc.wantID || resp.Result["title"] != tc.wantTitle {
				t.Fatalf("result = %v, want id %q title %q", resp.Result, tc.wantID, tc.wantTitle)
			}
			store, ok := tc.store.(*crudSessions)
			if !ok {
				t.Fatalf("store = %T, want *crudSessions", tc.store)
			}
			if store.renamedID != tc.wantID || store.renamedTitle != tc.wantTitle {
				t.Fatalf("renamed (%q, %q), want (%q, %q)", store.renamedID, store.renamedTitle, tc.wantID, tc.wantTitle)
			}
		})
	}
}

// TestRPCSessionDelete 覆盖 session.delete 的成功、非法 id、不存在、脱敏、
// 未配置，以及"活动会话不可删"的守卫（含前缀写法）。
func TestRPCSessionDelete(t *testing.T) {
	storage := filepath.Join(os.TempDir(), "argentgo", "sessions", "broken.jsonl")
	cases := []struct {
		name          string
		store         SessionStore
		runner        Runner
		params        string
		wantCode      int
		wantKeep      []string
		wantAbsent    []string
		wantDelete    string
		guardRejected bool
	}{
		{
			name:       "success",
			store:      &crudSessions{},
			params:     `{"id":"s1"}`,
			wantDelete: "s1",
		},
		{
			name:       "unrelated active session still deletes",
			store:      &crudSessions{},
			runner:     &sessionIDRunner{id: "active-9"},
			params:     `{"id":"s1"}`,
			wantDelete: "s1",
		},
		{
			name:          "missing id",
			store:         &crudSessions{},
			params:        `{}`,
			wantCode:      codeInvalidParams,
			wantKeep:      []string{"session id is required"},
			guardRejected: true,
		},
		{
			name:          "invalid params",
			store:         &crudSessions{},
			params:        `{"id":true}`,
			wantCode:      codeInvalidParams,
			guardRejected: true,
		},
		{
			name:       "not found error scrubbed",
			store:      &crudSessions{deleteErr: fmt.Errorf("session: delete %s: no such file", storage)},
			params:     `{"id":"missing"}`,
			wantCode:   codeServerError,
			wantKeep:   []string{"delete session", "missing", "<storage>/..."},
			wantAbsent: []string{storage, filepath.ToSlash(storage)},
		},
		{
			name:       "credential scrubbed",
			store:      &crudSessions{deleteErr: fmt.Errorf("session: delete %s: denied sk-secret123456789", storage)},
			params:     `{"id":"s1"}`,
			wantCode:   codeServerError,
			wantKeep:   []string{"<storage>/...", "denied"},
			wantAbsent: []string{storage, filepath.ToSlash(storage), "sk-secret123456789"},
		},
		{
			name:     "not configured",
			store:    fakeSessions{},
			params:   `{"id":"s1"}`,
			wantCode: codeServerError,
			wantKeep: []string{"session delete not configured"},
		},
		{
			name:     "no store",
			params:   `{"id":"s1"}`,
			wantCode: codeServerError,
			wantKeep: []string{"session store not configured"},
		},
		{
			name:          "active session rejected",
			store:         &crudSessions{},
			runner:        &sessionIDRunner{id: "active-1"},
			params:        `{"id":"active-1"}`,
			wantCode:      codeServerError,
			wantKeep:      []string{"cannot delete active session", "active-1"},
			guardRejected: true,
		},
		{
			name:          "active session rejected by prefix",
			store:         &crudSessions{},
			runner:        &sessionIDRunner{id: "20260123-123456-abcd"},
			params:        `{"id":"20260123"}`,
			wantCode:      codeServerError,
			wantKeep:      []string{"cannot delete active session"},
			guardRejected: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := tc.runner
			if runner == nil {
				runner = &fakeRunner{}
			}
			resp := runCRUDRequest(t, Config{Runner: runner, Sessions: tc.store}, crudRequest("session.delete", tc.params))
			if tc.wantCode != 0 {
				assertCRUDError(t, resp, tc.wantCode, tc.wantKeep, tc.wantAbsent)
				if tc.guardRejected {
					if store, ok := tc.store.(*crudSessions); ok && store.deletedID != "" {
						t.Fatalf("delete was called with %q despite guard rejection", store.deletedID)
					}
				}
				return
			}
			if resp.Error != nil {
				t.Fatalf("unexpected error: %+v", resp.Error)
			}
			if resp.Result["ok"] != true {
				t.Fatalf("result = %v, want ok:true", resp.Result)
			}
			store, ok := tc.store.(*crudSessions)
			if !ok {
				t.Fatalf("store = %T, want *crudSessions", tc.store)
			}
			if store.deletedID != tc.wantDelete {
				t.Fatalf("deleted id = %q, want %q", store.deletedID, tc.wantDelete)
			}
		})
	}
}

// TestScrubSessionErrorVerbs 锁定三种写操作与 load 的错误前缀，并确认
// create（尚无 id）与 delete（折叠路径）两路的脱敏边界与 load 一致。
func TestScrubSessionErrorVerbs(t *testing.T) {
	windowsPath := `C:\Users\tester\.argentgo\sessions\abc.jsonl`
	cases := []struct {
		name string
		verb string
		id   string
		err  error
		want string
	}{
		{"load keeps id", "load", "s1", errors.New("boom"), `load session "s1": boom`},
		{"create has no id", "create", "", errors.New("boom"), "create session: boom"},
		{"rename keeps id", "rename", "s1", errors.New("boom"), `rename session "s1": boom`},
		{"delete keeps id", "delete", "s1", errors.New("boom"), `delete session "s1": boom`},
		{"delete folds path", "delete", "s1", fmt.Errorf("read %s: denied", windowsPath), `delete session "s1": read <storage>/...: denied`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubSessionError(tc.verb, tc.id, tc.err); got != tc.want {
				t.Fatalf("scrubSessionError = %q, want %q", got, tc.want)
			}
		})
	}
}
