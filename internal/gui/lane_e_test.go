package gui

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fakeHistory struct {
	undoCalls  int
	retryCalls int
	sid        string
	steps      int
}

func (h *fakeHistory) Undo(_ context.Context, sid string, steps int) error {
	h.undoCalls++
	h.sid, h.steps = sid, steps
	return nil
}

func (h *fakeHistory) Retry(_ context.Context, sid string, steps int) error {
	h.retryCalls++
	h.sid, h.steps = sid, steps
	return nil
}

func TestUndoAndRetryDispatchThroughTheHistorySeam(t *testing.T) {
	history := &fakeHistory{}
	env := newTestEnv(t, Options{History: history})
	for _, command := range []string{"undo", "retry"} {
		if result := env.object(command, `{"sid":"7","steps":2}`); len(result) != 0 {
			t.Fatalf("%s returned %v", command, result)
		}
	}
	if history.undoCalls != 1 || history.retryCalls != 1 || history.sid != "7" || history.steps != 2 {
		t.Fatalf("history calls = %+v", history)
	}
}

func TestUndoAndRetryWithoutHistoryFailLoudly(t *testing.T) {
	env := newTestEnv(t, Options{})
	for _, command := range []string{"undo", "retry"} {
		message := env.commandError(command, `{"sid":"1"}`)
		if !strings.Contains(message, "checkpoint store") {
			t.Fatalf("%s without history = %q", command, message)
		}
	}
}

func TestAttemptBadgesSerializeInAgentAndLoadedFrames(t *testing.T) {
	agentFrame, ok := Event{Kind: EventAgent, Agent: &AgentEvent{
		Type: AgentAttempt, Attempt: "stream_error", Text: "sse reset",
	}}.payload()
	agentPayload, _ := agentFrame["event"].(map[string]any)
	if !ok || agentPayload["attempt"] != "stream_error" {
		t.Fatalf("agent attempt frame = %v", agentFrame)
	}
	loaded := LoadedEntry{Kind: "tool", CallID: "1", Name: "write", Attempt: "retried"}
	frame := loaded.payload()
	if frame["attempt"] != "retried" || frame["callId"] != "1" {
		t.Fatalf("loaded attempt frame = %v", frame)
	}
}

func TestAssetCookieNeverAuthorizesStateChangingRpc(t *testing.T) {
	env := newTestEnv(t, Options{})
	request, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/?token="+env.bridge.token, nil)
	_, page := do(t, request)
	var cookie *http.Cookie
	for _, candidate := range readCookies(t, env) {
		if candidate.Name == "argent_token" {
			cookie = candidate
		}
	}
	if cookie == nil || cookie.Value != env.bridge.token {
		t.Fatalf("asset cookie missing from index response: %s", page)
	}
	// A HEAD asset request with only the cookie still works; the same cookie
	// must never authorise a POST. This is the E18 asset-auth regression.
	assetMatch := regexp.MustCompile(`src="(/assets/[^"]+)"`).FindSubmatch(page)
	if len(assetMatch) != 2 {
		t.Fatalf("index has no module asset: %s", page)
	}
	assetRequest, _ := http.NewRequest(http.MethodHead, env.srv.URL+string(assetMatch[1]), nil)
	assetRequest.AddCookie(cookie)
	if status, _ := do(t, assetRequest); status != http.StatusOK {
		t.Fatalf("HEAD asset with cookie = %d", status)
	}
	rpcRequest, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/rpc/get_state", strings.NewReader("{}"))
	rpcRequest.AddCookie(cookie)
	if status, _ := do(t, rpcRequest); status != http.StatusUnauthorized {
		t.Fatalf("POST /rpc with cookie only = %d, want 401", status)
	}
}

func readCookies(t *testing.T, env *testEnv) []*http.Cookie {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/?token="+env.bridge.token, nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.Cookies()
}

func TestStdlibWorkflowAndDocumentDefaults(t *testing.T) {
	dir := t.TempDir()
	workflow := `{"name":"review","steps":[{"name":"a","prompt":"go"}]}`
	if err := os.WriteFile(filepath.Join(dir, "review.json"), []byte(workflow), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	docPath := filepath.Join(dir, "report.docx")
	if err := writeTinyDocx(docPath, "docx text"); err != nil {
		t.Fatal(err)
	}
	env := newTestEnv(t, Options{Dir: dir})
	var workflows []WorkflowRow
	rpcDecode(t, env, "list_workflows", "", &workflows)
	if len(workflows) != 1 || workflows[0].Name != "review" || !workflows[0].Validation.OK {
		t.Fatalf("list_workflows = %+v", workflows)
	}
	var documents []DocumentRow
	rpcDecode(t, env, "list_documents", "", &documents)
	if len(documents) != 2 {
		t.Fatalf("list_documents = %+v", documents)
	}
	var extracted string
	rpcDecode(t, env, "read_document", `{"path":"report.docx"}`, &extracted)
	if !strings.Contains(extracted, "docx text") {
		t.Fatalf("read_document = %q", extracted)
	}
	escaped := env.commandError("read_document", `{"path":"../secret.docx"}`)
	if !strings.Contains(escaped, "outside") {
		t.Fatalf("path escape error = %q", escaped)
	}
}

func writeTinyDocx(path, text string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := zip.NewWriter(file)
	part, err := writer.Create("word/document.xml")
	if err != nil {
		return err
	}
	_, err = part.Write([]byte(`<w:document xmlns:w="x"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`))
	if err != nil {
		return err
	}
	return writer.Close()
}

// listenFrames drives the real SSE route with an HTTP client; no browser is
// needed to prove the stream carries attempt badges and usage counters.
func listenFrames(baseURL, token string, out chan<- map[string]any) (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/events?token="+token, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	go func() {
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err == nil {
				select {
				case out <- frame:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return cancel, nil
}

func TestTurnDoneAndAttemptFramesShareTheSseBroker(t *testing.T) {
	env := newTestEnv(t, Options{})
	runner := env.factory.last()
	seen := make(chan map[string]any, 4)
	unlisten, err := listenFrames(env.srv.URL, env.bridge.token, seen)
	if err != nil {
		t.Fatal(err)
	}
	defer unlisten()
	runner.events <- Event{Kind: EventAgent, Agent: &AgentEvent{
		Type: AgentToolFinished, ID: "1", Name: "write", Output: "ok",
	}}
	runner.events <- Event{Kind: EventAgent, Agent: &AgentEvent{
		Type: AgentAttempt, Attempt: "failed", Text: "write failed",
	}}
	runner.events <- Event{Kind: EventTurnDone, TurnDone: &TurnDone{
		Reason: "complete", PromptTokens: 10, CachedTokens: 4,
	}}
	for _, want := range []string{"tool_finished", "attempt", "turn_done"} {
		found := false
		for i := 0; i < 3 && !found; i++ {
			frame := <-seen
			agent, _ := frame["event"].(map[string]any)
			found = frame["kind"] == "turn_done" && want == "turn_done" ||
				agent["type"] == want
			if want == "turn_done" && frame["cachedTokens"] != float64(4) {
				t.Fatalf("turn_done cached tokens = %v", frame["cachedTokens"])
			}
		}
		if !found {
			t.Fatalf("frame %q not received", want)
		}
	}
}
