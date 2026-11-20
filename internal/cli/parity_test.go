package cli

import (
	"bytes"
	"io"
	"sort"
	"strings"
	"testing"
)

// expectedFlags is the machine-checked flag parity gate. Every entry must be
// registered by newFlags, and every entry must be documented in writeUsage.
var expectedFlags = []string{
	"add-dir",
	"acp",
	"api-key",
	"approval",
	"background",
	"base-url",
	"config",
	"continue",
	"continue-session",
	"cwd",
	"enable-browser",
	"enable-desktop",
	"enable-service",
	"fallback-model",
	"gui",
	"help",
	"json-lines",
	"list-models",
	"list-profiles",
	"list-sessions",
	"locale",
	"max-cost",
	"max-parallel",
	"max-seconds",
	"max-tokens",
	"max-turns",
	"mode",
	"model",
	"no-auto-compact",
	"no-color",
	"no-prompt-cache",
	"output-schema",
	"plugins",
	"price-input",
	"price-output",
	"profile",
	"protocol",
	"provider",
	"resume",
	"retry",
	"rpc",
	"sandbox",
	"sandbox-mode",
	"secondary-model",
	"serve",
	"serve-address",
	"serve-port",
	"serve-token",
	"session",
	"session-id",
	"theme",
	"thinking",
	"title",
	"token",
	"tokens",
	"trust-project",
	"tui",
	"undo",
	"version",
	"workflow-journal",
	"workflow-mode",
	"workflow-resume",
	"worktree",
	"yolo",
}

// expectedCommands is the subcommand parity gate.
var expectedCommands = []string{
	"audit",
	"attach",
	"auth",
	"board",
	"bug",
	"commands",
	"completion",
	"config",
	"doctor",
	"eval",
	"follow",
	"help",
	"hooks",
	"jobs",
	"login",
	"logout",
	"logs",
	"mcp",
	"models",
	"policy",
	"replay",
	"sessions",
	"skills",
	"stop",
	"telemetry",
	"tui",
	"update",
	"usage",
	"version",
	"workflow",
}

func TestFlagSurfaceMatchesParityList(t *testing.T) {
	got := flagSurface()
	want := sortedStrings(expectedFlags)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("flag surface drifted:\n got: %v\nwant: %v", got, want)
	}
}

func TestSubcommandSurfaceMatchesParityList(t *testing.T) {
	want := sortedStrings(expectedCommands)
	if got := subcommandNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("subcommand list drifted:\n got: %v\nwant: %v", got, want)
	}
	mapKeys := make([]string, 0, len(subcommands))
	for name := range subcommands {
		mapKeys = append(mapKeys, name)
	}
	sort.Strings(mapKeys)
	if strings.Join(mapKeys, ",") != strings.Join(want, ",") {
		t.Fatalf("dispatch map does not match the parity list:\n map: %v\nwant: %v", mapKeys, want)
	}
}

func TestUsageTextDocumentsEveryFlag(t *testing.T) {
	var out bytes.Buffer
	writeUsage(&out)
	usage := out.String()
	for _, name := range expectedFlags {
		if !strings.Contains(usage, "--"+name) {
			t.Errorf("usage text does not mention --%s", name)
		}
	}
}

func TestUsageTextMentionsEveryCommand(t *testing.T) {
	var out bytes.Buffer
	writeUsage(&out)
	usage := out.String()
	for _, name := range expectedCommands {
		if name == "help" || name == "version" {
			continue // covered by the flag lines (-h/--help, -V/--version)
		}
		if !strings.Contains(usage, name) {
			t.Errorf("usage text does not mention the %q command", name)
		}
	}
}

func TestParseNewFlagSurface(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want func(*testing.T, *Options)
	}{
		{
			name: "provider and profile",
			args: []string{"--provider", "anthropic", "--profile", "work"},
			want: func(t *testing.T, o *Options) {
				if o.Provider != "anthropic" || o.Profile != "work" {
					t.Fatalf("provider=%q profile=%q", o.Provider, o.Profile)
				}
			},
		},
		{
			name: "budget flags",
			args: []string{"--max-tokens", "900", "--max-cost", "0.5", "--max-seconds", "30",
				"--price-input", "3", "--price-output", "15"},
			want: func(t *testing.T, o *Options) {
				if o.MaxTokens != 900 || o.MaxCost != 0.5 || o.MaxSeconds != 30 {
					t.Fatalf("budget = %+v", o)
				}
				if o.PriceInput != 3 || o.PriceOutput != 15 {
					t.Fatalf("prices = %v/%v", o.PriceInput, o.PriceOutput)
				}
			},
		},
		{
			name: "models and retries",
			args: []string{"--fallback-model", "b", "--secondary-model", "c", "--thinking", "high"},
			want: func(t *testing.T, o *Options) {
				if o.FallbackModel != "b" || o.SecondaryModel != "c" || o.Thinking != "high" {
					t.Fatalf("options = %+v", o)
				}
			},
		},
		{
			name: "repeatable dirs and plugins",
			args: []string{"--add-dir", "one", "--add-dir", "two", "--plugins", "p1", "--plugins", "all"},
			want: func(t *testing.T, o *Options) {
				if len(o.AddDirs) != 2 || o.AddDirs[0] != "one" || o.AddDirs[1] != "two" {
					t.Fatalf("add dirs = %v", o.AddDirs)
				}
				if len(o.Plugins) != 2 || o.Plugins[0] != "p1" || o.Plugins[1] != "all" {
					t.Fatalf("plugins = %v", o.Plugins)
				}
			},
		},
		{
			name: "json lines implies json mode",
			args: []string{"--json-lines"},
			want: func(t *testing.T, o *Options) {
				if !o.JSONLines || o.Mode != ModeJSON {
					t.Fatalf("json lines = %v mode = %q", o.JSONLines, o.Mode)
				}
			},
		},
		{
			name: "continue session implies resume and stays in text mode",
			args: []string{"--continue-session"},
			want: func(t *testing.T, o *Options) {
				if !o.Resume {
					t.Fatal("continue-session did not set resume")
				}
				// An empty Mode is the text default: the CLI only branches on
				// ModeJSON, so anything else is text output.
				mode := o.Mode
				if mode == "" {
					mode = ModeText
				}
				if o.JSONLines || mode != ModeText {
					t.Fatalf("continue-session must not enable JSON output: jsonLines=%v mode=%q", o.JSONLines, o.Mode)
				}
			},
		},
		{
			name: "sandbox alias",
			args: []string{"--sandbox", "read-only"},
			want: func(t *testing.T, o *Options) {
				if o.SandboxMode != SandboxReadOnly {
					t.Fatalf("sandbox = %q", o.SandboxMode)
				}
			},
		},
		{
			name: "worktree bare value",
			args: []string{"--worktree"},
			want: func(t *testing.T, o *Options) {
				if o.Worktree != "auto" {
					t.Fatalf("worktree = %q", o.Worktree)
				}
			},
		},
		{
			name: "worktree named value",
			args: []string{"--worktree=feat"},
			want: func(t *testing.T, o *Options) {
				if o.Worktree != "feat" {
					t.Fatalf("worktree = %q", o.Worktree)
				}
			},
		},
		{
			name: "listing and toggles",
			args: []string{"--list-profiles", "--list-sessions", "--list-models", "--tokens",
				"--trust-project", "--no-color", "--no-auto-compact", "--theme", "dark"},
			want: func(t *testing.T, o *Options) {
				if !o.ListProfiles || !o.ListSessions || !o.ListModels || !o.Tokens {
					t.Fatalf("listings = %+v", o)
				}
				if !o.TrustProject || !o.NoColor || !o.NoAutoCompact || o.Theme != "dark" {
					t.Fatalf("toggles = %+v", o)
				}
			},
		},
		{
			name: "compat transports",
			args: []string{"--enable-browser", "--enable-desktop", "--enable-service", "--gui",
				"--background", "--workflow-mode", "auto", "--workflow-resume",
				"--workflow-journal", "j.json", "--output-schema", `{"type":"object"}`},
			want: func(t *testing.T, o *Options) {
				if !o.EnableBrowser || !o.EnableDesktop || !o.EnableService || !o.GUI || !o.Background {
					t.Fatalf("compat flags = %+v", o)
				}
				if !o.WorkflowResume || o.WorkflowMode != "auto" || o.WorkflowJournal != "j.json" {
					t.Fatalf("workflow = %+v", o)
				}
				if !strings.Contains(o.OutputSchema, `"object"`) {
					t.Fatalf("schema = %q", o.OutputSchema)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, _, err := parseArgs(tc.args, func(string) string { return "" }, io.Discard)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			tc.want(t, opts)
		})
	}
}

func TestUnmigratedFlagsFailLoudly(t *testing.T) {
	cases := [][]string{
		{"--worktree"},
		{"--background"},
		{"--enable-browser"},
		{"--enable-desktop"},
		{"--enable-service"},
		{"--gui"},
		{"--workflow-mode", "auto"},
		{"--workflow-resume"},
		{"--workflow-journal", "j.json"},
	}
	for _, args := range cases {
		t.Run(args[0], func(t *testing.T) {
			ioState, out, errOut := newTestIO("", nil)
			code := RunWith(App{IO: ioState}, append(args, "hello"))
			if code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout = %q", out.String())
			}
			if !strings.Contains(errOut.String(), "not migrated yet") {
				t.Fatalf("stderr = %q", errOut.String())
			}
		})
	}
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
