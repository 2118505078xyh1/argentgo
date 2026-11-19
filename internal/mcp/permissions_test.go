package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func toolNames(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

func TestSpecEnabledDefaultAndConnectRejectsDisabled(t *testing.T) {
	off, on := false, true
	if !(Spec{Name: "x"}).IsEnabled() {
		t.Fatal("zero-value spec must be enabled")
	}
	if (Spec{Name: "x", Enabled: &off}).IsEnabled() {
		t.Fatal("explicit false must disable the spec")
	}
	if !(Spec{Name: "x", Enabled: &on}).IsEnabled() {
		t.Fatal("explicit true must enable the spec")
	}
	if _, err := Connect(context.Background(), Spec{Name: "x", Enabled: &off}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled server should not connect: %v", err)
	}
}

func TestFilterToolsAllowDenyAndQualifiedNames(t *testing.T) {
	tools := []Tool{
		{Name: "search", Server: "fs"},
		{Name: "write", Server: "fs"},
		{Name: "read", Server: "fs"},
		{Name: "ping", Server: "web"},
	}
	cases := []struct {
		name            string
		allowed, denied []string
		want            []string
	}{
		{"empty allow means all", nil, nil, []string{"search", "write", "read", "ping"}},
		{"plain allow", []string{"search"}, nil, []string{"search"}},
		{"qualified allow", []string{"fs__search"}, nil, []string{"search"}},
		{"qualified allow other server", []string{"web__search"}, nil, []string{}},
		{"deny wins over allow", []string{"search", "fs__write"}, []string{"write"}, []string{"search"}},
		{"qualified deny", nil, []string{"web__ping"}, []string{"search", "write", "read"}},
		{"wildcards", []string{"fs__*"}, []string{"*__write"}, []string{"search", "read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := Spec{Name: "fs", AllowedTools: tc.allowed, DeniedTools: tc.denied}
			got := toolNames(FilterTools(spec, tools))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tools = %v, want %v", got, tc.want)
			}
			again := toolNames(FilterTools(spec, tools))
			if !reflect.DeepEqual(got, again) {
				t.Fatalf("filter order is not stable: %v vs %v", got, again)
			}
		})
	}
}

func TestFilterToolsMatchesQualifiedAdvertisedName(t *testing.T) {
	tools := []Tool{{Name: "fs__search", Server: "fs"}, {Name: "fs__write", Server: "fs"}}
	spec := Spec{Name: "fs", AllowedTools: []string{"search"}, DeniedTools: []string{"fs__write"}}
	got := toolNames(FilterTools(spec, tools))
	if !reflect.DeepEqual(got, []string{"fs__search"}) {
		t.Fatalf("qualified advertised names not matched: %v", got)
	}
}

func TestDisabledSpecExposesNoTools(t *testing.T) {
	off := false
	spec := Spec{Name: "fs", Enabled: &off}
	if got := FilterTools(spec, []Tool{{Name: "search"}}); len(got) != 0 {
		t.Fatalf("disabled spec exposed tools: %+v", got)
	}
	if got := EnabledSpecs([]Spec{spec, {Name: "web"}}); len(got) != 1 || got[0].Name != "web" {
		t.Fatalf("EnabledSpecs = %+v", got)
	}
}

func TestListToolsAndClientViewsHonourPermissions(t *testing.T) {
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/list": func(json.RawMessage) (any, error) {
			return map[string]any{"tools": []any{
				map[string]any{"name": "search"},
				map[string]any{"name": "write"},
				map[string]any{"name": "read"},
			}}, nil
		},
	}}
	c := newFakeClient("fs", ft, time.Second)
	c.spec.AllowedTools = []string{"search", "write"}
	c.spec.DeniedTools = []string{"write"}

	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if got := toolNames(tools); !reflect.DeepEqual(got, []string{"search"}) {
		t.Fatalf("ListTools = %v, want [search]", got)
	}
	if got := toolNames(c.Tools()); !reflect.DeepEqual(got, []string{"search"}) {
		t.Fatalf("Tools cache = %v, want [search]", got)
	}
	if specs := (Clients{c}).Specs(); len(specs) != 1 || specs[0].Name != "fs" {
		t.Fatalf("Clients.Specs = %+v", specs)
	}
	if got := toolNames((Clients{c}).Tools()); !reflect.DeepEqual(got, []string{"search"}) {
		t.Fatalf("Clients.Tools = %v", got)
	}

	off := false
	c.spec.Enabled = &off
	if len(c.Specs()) != 0 || len((Clients{c}).Specs()) != 0 || len((Clients{c}).Tools()) != 0 {
		t.Fatal("disabled client must disappear from all views")
	}
}

func TestReadOnlyBlocksMutatingCallsBeforeTransport(t *testing.T) {
	var calls []string
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(params json.RawMessage) (any, error) {
			var call struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(params, &call); err != nil {
				return nil, err
			}
			calls = append(calls, call.Name)
			return map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}, nil
		},
	}}
	c := newFakeClient("fs", ft, time.Second)
	c.spec.ReadOnly = true
	MarkMutating("blocked_op")

	for _, name := range []string{"write_file", "fs__delete_file", "writeFile", "blocked_op"} {
		_, err := c.CallTool(context.Background(), name, nil)
		if !errors.Is(err, ErrReadOnly) {
			t.Fatalf("%q: expected ErrReadOnly, got %v", name, err)
		}
		if !strings.Contains(err.Error(), "no request was sent") || !strings.Contains(err.Error(), name) {
			t.Fatalf("%q: unclear error: %v", name, err)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("mutating calls reached the transport: %v", calls)
	}
	if _, err := c.CallTool(context.Background(), "search", nil); err != nil {
		t.Fatalf("read-only server must still allow search: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"search"}) {
		t.Fatalf("transport calls = %v, want [search]", calls)
	}
}

func TestCallToolRejectsFilteredToolsBeforeTransport(t *testing.T) {
	var called bool
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) {
			called = true
			return map[string]any{}, nil
		},
	}}
	c := newFakeClient("fs", ft, time.Second)
	c.spec.DeniedTools = []string{"fs__write"}
	if _, err := c.CallTool(context.Background(), "write", nil); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("denied tool should be blocked: %v", err)
	}
	c.spec.DeniedTools = nil
	c.spec.AllowedTools = []string{"search"}
	if _, err := c.CallTool(context.Background(), "write", nil); !errors.Is(err, ErrToolNotAllowed) {
		t.Fatalf("tool outside allowlist should be blocked: %v", err)
	}
	if called {
		t.Fatal("filtered tool reached the transport")
	}
}

func TestIsMutatingHeuristicAndRegistry(t *testing.T) {
	cases := []struct {
		name     string
		mutating bool
	}{
		{"write_file", true},
		{"read_file", false},
		{"fs__write_file", true},
		{"writeFile", true},
		{"get_status", false},
		{"search", false},
		{"git_commit", true},
		{"write2file", true},
		{"widget_gadget", false},
	}
	for _, tc := range cases {
		if got := IsMutating(tc.name); got != tc.mutating {
			t.Errorf("IsMutating(%q) = %v, want %v", tc.name, got, tc.mutating)
		}
	}
	MarkMutating("widget_gadget")
	for _, name := range []string{"widget_gadget", "prod__widget_gadget", "WIDGET_GADGET"} {
		if !IsMutating(name) {
			t.Errorf("registered mutating name %q was not matched", name)
		}
	}
}
