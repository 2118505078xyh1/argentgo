package mcp

import (
	"reflect"
	"strings"
	"testing"
)

func TestSpecsFromConfigCamelAndSnakeKeys(t *testing.T) {
	raw := map[string]any{
		"mcpServers": map[string]any{
			"camel": map[string]any{
				"command":      "node",
				"args":         []any{"server.js"},
				"env":          map[string]any{"A": "B"},
				"url":          "http://localhost:1",
				"enabled":      true,
				"readOnly":     true,
				"allowedTools": []any{"search", "fs__read"},
				"deniedTools":  []any{"delete"},
			},
			"snake": map[string]any{
				"url":           "https://example.test",
				"enabled":       false,
				"read_only":     true,
				"allowed_tools": []string{"list"},
				"denied_tools":  []string{"write"},
				"args":          []string{"--stdio"},
			},
		},
	}
	specs := SpecsFromConfig(raw)
	if len(specs) != 2 {
		t.Fatalf("specs = %+v, want 2", specs)
	}
	byName := map[string]Spec{}
	for _, spec := range specs {
		byName[spec.Name] = spec
	}

	camel := byName["camel"]
	if camel.Command != "node" || camel.URL != "http://localhost:1" {
		t.Fatalf("camel target lost: %+v", camel)
	}
	if !reflect.DeepEqual(camel.Args, []string{"server.js"}) || !reflect.DeepEqual(camel.Env, []string{"A=B"}) {
		t.Fatalf("camel args/env lost: %+v", camel)
	}
	if camel.Enabled == nil || !*camel.Enabled || !camel.ReadOnly {
		t.Fatalf("camel permissions lost: %+v", camel)
	}
	if !reflect.DeepEqual(camel.AllowedTools, []string{"search", "fs__read"}) ||
		!reflect.DeepEqual(camel.DeniedTools, []string{"delete"}) {
		t.Fatalf("camel tool lists lost: %+v", camel)
	}

	snake := byName["snake"]
	if snake.URL != "https://example.test" || !reflect.DeepEqual(snake.Args, []string{"--stdio"}) {
		t.Fatalf("snake target lost: %+v", snake)
	}
	if snake.Enabled == nil || *snake.Enabled || !snake.ReadOnly {
		t.Fatalf("snake permissions lost: %+v", snake)
	}
	if !reflect.DeepEqual(snake.AllowedTools, []string{"list"}) ||
		!reflect.DeepEqual(snake.DeniedTools, []string{"write"}) {
		t.Fatalf("snake tool lists lost: %+v", snake)
	}
}

func TestDecodeConfigSkipsMalformedEntriesWithProblems(t *testing.T) {
	raw := map[string]any{
		"mcpServers": map[string]any{
			"good": map[string]any{
				"command":  "go",
				"args":     []any{"run", "."},
				"enabled":  "false",
				"readOnly": "yes",
			},
			"bad-type": "not-an-object",
			"bad-fields": map[string]any{
				"command":      42,
				"args":         []any{"x", 7},
				"env":          "NOPE",
				"allowedTools": 5,
				"readOnly":     "maybe",
			},
			"": map[string]any{"command": "x"},
		},
	}
	specs, problems := DecodeConfig(raw)
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	if !reflect.DeepEqual(names, []string{"bad-fields", "good"}) {
		t.Fatalf("specs = %v, want [bad-fields good]", names)
	}
	good := specs[1]
	if good.Enabled == nil || *good.Enabled || !good.ReadOnly {
		t.Fatalf("string booleans not decoded: %+v", good)
	}
	if !reflect.DeepEqual(good.Args, []string{"run", "."}) {
		t.Fatalf("valid args lost: %+v", good)
	}

	var notes []string
	for _, problem := range problems {
		notes = append(notes, problem.Error())
	}
	joined := strings.Join(notes, " | ")
	for _, want := range []string{
		"want a string, got", "want an array of strings", "want an object or array of strings", "want a boolean",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing problem %q in: %s", want, joined)
		}
	}
}

func TestDecodeConfigAcceptsServerListAndMissingKey(t *testing.T) {
	raw := map[string]any{
		"mcpServers": []any{
			map[string]any{"name": "one", "command": "a"},
			"skip me",
			map[string]any{"command": "b"},
		},
	}
	specs, problems := DecodeConfig(raw)
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	if !reflect.DeepEqual(names, []string{"one", "server-3"}) {
		t.Fatalf("list-form specs = %v", names)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "#2") {
		t.Fatalf("list-form problems = %+v", problems)
	}
	if got := SpecsFromConfig(map[string]any{"other": 1}); len(got) != 0 {
		t.Fatalf("missing mcpServers should decode to nothing: %+v", got)
	}
}
