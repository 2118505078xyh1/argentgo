package gui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// sseClient reads the data lines of /events into a channel so tests never
// block forever on a missing frame.
type sseClient struct {
	frames chan map[string]any
}

func openEvents(t *testing.T, srv *httptest.Server, token string) *sseClient {
	t.Helper()
	response, err := http.Get(srv.URL + "/events?token=" + url.QueryEscape(token))
	if err != nil {
		t.Fatalf("connect /events: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("connect /events = %d", response.StatusCode)
	}
	client := &sseClient{frames: make(chan map[string]any, 128)}
	// SSE is an infinite response. Without closing the body at test end,
	// httptest.Server.Close blocks forever waiting for this connection.
	t.Cleanup(func() { _ = response.Body.Close() })
	go func() {
		defer close(client.frames)
		defer response.Body.Close()
		reader := bufio.NewReader(response.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var frame map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
				continue
			}
			select {
			case client.frames <- frame:
			default:
			}
		}
	}()
	return client
}

func (c *sseClient) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case frame, ok := <-c.frames:
		if !ok {
			t.Fatal("event stream closed before the expected frame")
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an event frame")
		return nil
	}
}

func (c *sseClient) expectNone(t *testing.T) {
	t.Helper()
	select {
	case frame := <-c.frames:
		t.Fatalf("unexpected event frame: %v", frame)
	case <-time.After(120 * time.Millisecond):
	}
}

func nextCommand(t *testing.T, runner *fakeRunner) Command {
	t.Helper()
	select {
	case command := <-runner.commands:
		return command
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a worker command")
		return Command{}
	}
}

func TestPromptRoundTripStreamsTaggedFrames(t *testing.T) {
	env := newTestEnv(t, Options{})
	runner := env.factory.last()
	stream := openEvents(t, env.srv, env.bridge.token)

	status, body := env.rpc("send_prompt", `{"sid":"1","text":"hello"}`)
	if status != http.StatusOK || strings.Contains(string(body), `"error"`) {
		t.Fatalf("send_prompt = %d %s", status, body)
	}
	command := nextCommand(t, runner)
	if command.Kind != CommandPrompt || command.Text != "hello" {
		t.Fatalf("worker got %+v, want prompt hello", command)
	}

	runner.events <- Event{Kind: EventAgent, Agent: &AgentEvent{Type: AgentText, Text: "hel"}}
	runner.events <- Event{Kind: EventAgent, Agent: &AgentEvent{Type: AgentText, Text: "lo"}}
	cost := 0.5
	runner.events <- Event{Kind: EventTurnDone, TurnDone: &TurnDone{
		Reason: "complete", Requests: 2, Cost: &cost,
		PromptTokens: 12, CompletionTokens: 3, TotalTokens: 15, CachedTokens: 4,
	}}

	var text string
	for i := 0; i < 2; i++ {
		frame := stream.next(t)
		if frame["kind"] != "agent" || frame["sid"] != "1" {
			t.Fatalf("frame %d = %v, want an agent frame tagged sid 1", i, frame)
		}
		event := frame["event"].(map[string]any)
		if event["type"] != AgentText {
			t.Fatalf("agent event = %v", event)
		}
		text += event["text"].(string)
	}
	if text != "hello" {
		t.Fatalf("streamed text = %q, want hello", text)
	}
	done := stream.next(t)
	if done["kind"] != "turn_done" || done["sid"] != "1" || done["reason"] != "complete" {
		t.Fatalf("turn_done = %v", done)
	}
	if done["promptTokens"] != float64(12) || done["completionTokens"] != float64(3) ||
		done["totalTokens"] != float64(15) || done["cachedTokens"] != float64(4) ||
		done["requests"] != float64(2) || done["cost"] != cost || done["priced"] != true {
		t.Fatalf("turn_done usage = %v", done)
	}
}

func TestTabLifecycleSwitchesAndPreservesTheLastTab(t *testing.T) {
	env := newTestEnv(t, Options{})
	if sid := env.text("new_tab", ""); sid != "2" {
		t.Fatalf("new_tab = %q, want 2", sid)
	}
	if env.factory.last() == nil || env.factory.last() == env.factory.at(0) {
		t.Fatal("new_tab must create a second worker")
	}
	if state := env.object("get_state", ""); state["sid"] != "2" {
		t.Fatalf("active tab after new_tab = %v", state["sid"])
	}
	if err := env.commandError("activate_tab", `{"sid":"1"}`); err != "" {
		t.Fatalf("activate_tab error = %q", err)
	}
	if state := env.object("get_state", ""); state["sid"] != "1" {
		t.Fatalf("active tab after activate_tab = %v", state["sid"])
	}
	if err := env.commandError("close_tab", `{"sid":"2"}`); err != "" {
		t.Fatalf("close_tab error = %q", err)
	}
	if command := nextCommand(t, env.factory.at(1)); command.Kind != CommandShutdown {
		t.Fatalf("closed worker got %+v, want shutdown", command)
	}
	if err := env.commandError("close_tab", `{"sid":"1"}`); err != "cannot close the last tab" {
		t.Fatalf("closing the last tab = %q", err)
	}
	if sid := env.text("new_tab", ""); sid != "3" {
		t.Fatalf("tab ids must not be reused, got %q", sid)
	}
	if err := env.commandError("activate_tab", `{"sid":"404"}`); err == "" {
		t.Fatal("activating a missing tab should fail")
	}
}

func TestApprovalFlowAnswersAndRemembersAllowedTools(t *testing.T) {
	env := newTestEnv(t, Options{})
	runner := env.factory.last()
	stream := openEvents(t, env.srv, env.bridge.token)

	reply := make(chan bool, 1)
	runner.approvals <- Approval{Name: "write", Arguments: `{"path":"a.txt"}`, Reply: reply}
	frame := stream.next(t)
	if frame["kind"] != "approval" || frame["sid"] != "1" || frame["name"] != "write" ||
		frame["arguments"] != `{"path":"a.txt"}` {
		t.Fatalf("approval frame = %v", frame)
	}
	id := frame["id"].(float64)
	if id < 1 {
		t.Fatalf("approval id = %v", id)
	}
	request := fmt.Sprintf(`{"sid":"1","id":%.0f,"allow":true}`, id)
	if err := env.commandError("answer_approval", request); err != "" {
		t.Fatalf("answer_approval error = %q", err)
	}
	select {
	case allowed := <-reply:
		if !allowed {
			t.Fatal("approval answer did not reach the worker")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approval answer never reached the worker")
	}
	if err := env.commandError("answer_approval", request); err != "approval is no longer pending" {
		t.Fatalf("answering twice = %q", err)
	}

	if err := env.commandError("allow_tool", `{"sid":"1","name":"read"}`); err != "" {
		t.Fatalf("allow_tool error = %q", err)
	}
	autoReply := make(chan bool, 1)
	runner.approvals <- Approval{Name: "read", Arguments: "{}", Reply: autoReply}
	select {
	case allowed := <-autoReply:
		if !allowed {
			t.Fatal("an allowed tool should be approved automatically")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("allowed tool was never answered")
	}
	stream.expectNone(t)
}

func TestLoadedAndSessionEventsKeepTheirWireShape(t *testing.T) {
	env := newTestEnv(t, Options{})
	runner := env.factory.last()
	stream := openEvents(t, env.srv, env.bridge.token)

	duration := int64(12)
	runner.events <- Event{Kind: EventLoaded, Entries: []LoadedEntry{
		{Kind: "user", Text: "hi"},
		{Kind: "tool", CallID: "c1", Name: "read", Arguments: "{}", Output: "ok",
			State: "ok", DurationMS: &duration},
	}}
	loaded := stream.next(t)
	if loaded["kind"] != "loaded" || loaded["sid"] != "1" {
		t.Fatalf("loaded frame = %v", loaded)
	}
	entries := loaded["entries"].([]any)
	if len(entries) != 2 || entries[1].(map[string]any)["callId"] != "c1" ||
		entries[1].(map[string]any)["durationMs"] != float64(12) {
		t.Fatalf("loaded entries = %v", entries)
	}

	runner.events <- Event{Kind: EventSession, Path: "a.jsonl"}
	session := stream.next(t)
	if session["kind"] != "session" || session["path"] != "a.jsonl" || session["sid"] != "1" {
		t.Fatalf("session frame = %v", session)
	}
	runner.events <- Event{Kind: EventSessionCleared}
	if cleared := stream.next(t); cleared["kind"] != "session_cleared" || cleared["sid"] != "1" {
		t.Fatalf("session_cleared frame = %v", cleared)
	}
}
