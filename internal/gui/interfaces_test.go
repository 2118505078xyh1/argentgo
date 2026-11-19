package gui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// One fake per injected interface. Each records the call it received so the
// /rpc round-trip test can prove the bridge reached the injected backend.
type fakeJobs struct {
	started, stopped, tailed string
	lines                    int
}

func (f *fakeJobs) List() ([]JobRow, error) {
	return []JobRow{{ID: "j1", Command: "ls", Status: "running"}}, nil
}
func (f *fakeJobs) Tail(id string, lines int) (string, error) {
	f.tailed, f.lines = id, lines
	return "fake log", nil
}
func (f *fakeJobs) Stop(id string) error { f.stopped = id; return nil }
func (f *fakeJobs) Start(command string) (string, error) {
	f.started = command
	return "j2", nil
}

type fakeAutomation struct {
	created string
	toggled string
	preview string
	deleted string
	ran     string
	logged  string
}

func (f *fakeAutomation) List() ([]AutomationRow, error) {
	return []AutomationRow{{ID: "a1", Name: "nightly", Schedule: "daily 09:00"}}, nil
}
func (f *fakeAutomation) Create(name, schedule, prompt string) error { f.created = name; return nil }
func (f *fakeAutomation) Toggle(id string) error                     { f.toggled = id; return nil }
func (f *fakeAutomation) Delete(id string) error                     { f.deleted = id; return nil }
func (f *fakeAutomation) Run(id string) error                        { f.ran = id; return nil }
func (f *fakeAutomation) Log(id string) (string, error) {
	f.logged = id
	return "fake automation log", nil
}
func (f *fakeAutomation) Preview(schedule string) (string, error) {
	f.preview = schedule
	return "tomorrow 09:00", nil
}

type fakeMemory struct {
	scope, key, value string
	forgetKey         string
	query             string
}

func (f *fakeMemory) List() ([]MemoryRow, error) {
	return []MemoryRow{{Scope: "user", Key: "fake", Value: "row"}}, nil
}
func (f *fakeMemory) Add(scope, key, value string) error {
	f.scope, f.key, f.value = scope, key, value
	return nil
}
func (f *fakeMemory) Forget(scope, key string) error { f.forgetKey = key; return nil }
func (f *fakeMemory) Search(query string) ([]MemoryRow, error) {
	f.query = query
	return []MemoryRow{{Scope: "user", Key: "hit", Value: "found"}}, nil
}

type fakeWorkflows struct {
	shown string
}

func (f *fakeWorkflows) List() ([]WorkflowRow, error) {
	return []WorkflowRow{{Path: "wf.json", Name: "review", Validation: WorkflowValidation{OK: true, Steps: 2}}}, nil
}
func (f *fakeWorkflows) Show(path string) (string, error) { f.shown = path; return "{}", nil }

type fakeDocuments struct {
	read string
}

func (f *fakeDocuments) List() ([]DocumentRow, error) {
	return []DocumentRow{{Name: "a.docx", Path: "docs/a.docx"}}, nil
}
func (f *fakeDocuments) Read(path string) (string, error) { f.read = path; return "document text", nil }

type fakeUsage struct{}

func (f *fakeUsage) Summary() (UsageSummary, error) {
	return UsageSummary{Turns: 7, PromptTokens: 42, Models: []ModelUsage{}, Daily: []DailyUsage{}}, nil
}

type fakePTY struct {
	spawned         string
	cols, rows      int
	written, readID string
	killed          string
}

func (f *fakePTY) Spawn(command string, cols, rows int) (string, error) {
	f.spawned, f.cols, f.rows = command, cols, rows
	return "p1", nil
}
func (f *fakePTY) Write(id, text string) error { f.written = id + ":" + text; return nil }
func (f *fakePTY) Read(id string) (string, error) {
	f.readID = id
	return "pty output", nil
}
func (f *fakePTY) Kill(id string) error { f.killed = id; return nil }
func (f *fakePTY) List() ([]PtyRow, error) {
	return []PtyRow{{ID: "p1", Exited: false}}, nil
}

func rpcDecode(t *testing.T, env *testEnv, command, body string, target any) {
	t.Helper()
	status, data := env.rpc(command, body)
	if status != http.StatusOK {
		t.Fatalf("%s: status %d: %s", command, status, data)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("%s: decode %q: %v", command, data, err)
	}
}

func TestInjectedInterfacesRoundTripThroughRPC(t *testing.T) {
	jobs := &fakeJobs{}
	automation := &fakeAutomation{}
	memory := &fakeMemory{}
	workflows := &fakeWorkflows{}
	documents := &fakeDocuments{}
	usage := &fakeUsage{}
	pty := &fakePTY{}
	env := newTestEnv(t, Options{
		Jobs: jobs, Automation: automation, Memory: memory,
		Workflows: workflows, Documents: documents, Usage: usage, PTY: pty,
	})

	var id string
	rpcDecode(t, env, "start_job", `{"command":"echo hi"}`, &id)
	if id != "j2" || jobs.started != "echo hi" {
		t.Fatalf("start_job = %q, fake saw %q", id, jobs.started)
	}

	if message := env.commandError("create_automation", `{"name":"nightly","schedule":"daily 09:00","prompt":"x"}`); message != "" {
		t.Fatalf("create_automation error = %q", message)
	}
	if automation.created != "nightly" {
		t.Fatalf("create_automation fake saw %q", automation.created)
	}

	if message := env.commandError("add_memory", `{"scope":"user","key":"k","value":"v"}`); message != "" {
		t.Fatalf("add_memory error = %q", message)
	}
	if memory.scope != "user" || memory.key != "k" || memory.value != "v" {
		t.Fatalf("add_memory fake saw %+v", memory)
	}

	var workflowRows []WorkflowRow
	rpcDecode(t, env, "list_workflows", "", &workflowRows)
	if len(workflowRows) != 1 || workflowRows[0].Name != "review" || !workflowRows[0].Validation.OK {
		t.Fatalf("list_workflows = %+v", workflowRows)
	}

	var documentRows []DocumentRow
	rpcDecode(t, env, "list_documents", "", &documentRows)
	if len(documentRows) != 1 || documentRows[0].Path != "docs/a.docx" {
		t.Fatalf("list_documents = %+v", documentRows)
	}

	var summary UsageSummary
	rpcDecode(t, env, "usage_summary", "", &summary)
	if summary.Turns != 7 || summary.PromptTokens != 42 {
		t.Fatalf("usage_summary = %+v", summary)
	}

	rpcDecode(t, env, "pty_spawn", `{"command":"bash","cols":100,"rows":24}`, &id)
	if id != "p1" || pty.spawned != "bash" || pty.cols != 100 || pty.rows != 24 {
		t.Fatalf("pty_spawn = %q, fake saw %+v", id, pty)
	}

	// Exercise the remaining methods of the multi-command interfaces.
	var tail string
	rpcDecode(t, env, "job_tail", `{"id":"j1","lines":50}`, &tail)
	if tail != "fake log" || jobs.tailed != "j1" || jobs.lines != 50 {
		t.Fatalf("job_tail = %q, fake saw %+v", tail, jobs)
	}
	var preview string
	rpcDecode(t, env, "preview_schedule", `{"schedule":"daily 09:00"}`, &preview)
	if preview != "tomorrow 09:00" || automation.preview != "daily 09:00" {
		t.Fatalf("preview_schedule = %q, fake saw %q", preview, automation.preview)
	}
	var log string
	rpcDecode(t, env, "automation_log", `{"id":"a1"}`, &log)
	if log != "fake automation log" || automation.logged != "a1" {
		t.Fatalf("automation_log = %q, fake saw %q", log, automation.logged)
	}
	var memoryRows []MemoryRow
	rpcDecode(t, env, "search_memory", `{"query":"found"}`, &memoryRows)
	if len(memoryRows) != 1 || memoryRows[0].Key != "hit" || memory.query != "found" {
		t.Fatalf("search_memory = %+v, fake query %q", memoryRows, memory.query)
	}
	rpcDecode(t, env, "pty_read", `{"id":"p1"}`, &log)
	if log != "pty output" || pty.readID != "p1" {
		t.Fatalf("pty_read = %q, fake saw %+v", log, pty)
	}
}

func TestNilInjectedInterfacesReturnReadableErrors(t *testing.T) {
	env := newTestEnv(t, Options{})
	for _, command := range []string{
		"list_jobs", "start_job", "job_tail", "kill_job",
		"list_automations", "create_automation", "toggle_automation",
		"delete_automation", "run_automation", "automation_log", "preview_schedule",
		"pty_spawn", "pty_write", "pty_read", "pty_kill", "pty_list",
	} {
		message := env.commandError(command, "{}")
		if !strings.Contains(message, "not supported in the Go build") {
			t.Fatalf("%s with nil backend = %q", command, message)
		}
	}
	if message := env.commandError("totally_unknown", ""); !strings.Contains(message, "unknown command") {
		t.Fatalf("unknown command = %q", message)
	}
}
