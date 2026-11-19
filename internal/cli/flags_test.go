package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func newTestIO(in string, env map[string]string) (IO, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	return IO{
		In:  strings.NewReader(in),
		Out: out,
		Err: errOut,
		Env: func(key string) string { return env[key] },
	}, out, errOut
}

func TestParseArgsFlags(t *testing.T) {
	type check func(*testing.T, *Options, []string)
	cases := []struct {
		name  string
		args  []string
		env   map[string]string
		want  check
		fails bool
	}{
		{
			name: "short model",
			args: []string{"-m", "gpt-x", "hello"},
			want: func(t *testing.T, o *Options, pos []string) {
				if o.Model != "gpt-x" || len(pos) != 1 || pos[0] != "hello" {
					t.Fatalf("model=%q pos=%v", o.Model, pos)
				}
			},
		},
		{
			name: "long model equals form",
			args: []string{"--model=gpt-y"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Model != "gpt-y" {
					t.Fatalf("model = %q", o.Model)
				}
			},
		},
		{
			name: "interleaved flags after prompt",
			args: []string{"explain", "-m", "m1", "this"},
			want: func(t *testing.T, o *Options, pos []string) {
				if o.Model != "m1" {
					t.Fatalf("model = %q", o.Model)
				}
				if strings.Join(pos, " ") != "explain this" {
					t.Fatalf("positional = %v", pos)
				}
			},
		},
		{
			name: "base url and api key",
			args: []string{"--base-url", "http://127.0.0.1:1/v1", "--api-key", "k"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.BaseURL != "http://127.0.0.1:1/v1" || o.APIKey != "k" {
					t.Fatalf("base=%q key=%q", o.BaseURL, o.APIKey)
				}
			},
		},
		{
			name: "protocol",
			args: []string{"--protocol", "anthropic"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Protocol != ProtocolAnthropic {
					t.Fatalf("protocol = %q", o.Protocol)
				}
			},
		},
		{
			name:  "invalid protocol",
			args:  []string{"--protocol", "grpc"},
			fails: true,
		},
		{
			name: "yolo and max turns",
			args: []string{"--yolo", "--max-turns", "3"},
			want: func(t *testing.T, o *Options, _ []string) {
				if !o.Yolo || o.MaxTurns != 3 {
					t.Fatalf("yolo=%v turns=%d", o.Yolo, o.MaxTurns)
				}
			},
		},
		{
			name:  "invalid max turns",
			args:  []string{"--max-turns", "many"},
			fails: true,
		},
		{
			name: "mode json",
			args: []string{"--mode", "json"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Mode != ModeJSON {
					t.Fatalf("mode = %q", o.Mode)
				}
			},
		},
		{
			name:  "invalid mode",
			args:  []string{"--mode", "yaml"},
			fails: true,
		},
		{
			name: "cwd short alias",
			args: []string{"-C", "sub"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Cwd != "sub" {
					t.Fatalf("cwd = %q", o.Cwd)
				}
			},
		},
		{
			name: "sandbox mode",
			args: []string{"--sandbox-mode", "read-only"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.SandboxMode != SandboxReadOnly {
					t.Fatalf("sandbox = %q", o.SandboxMode)
				}
			},
		},
		{
			name:  "invalid sandbox mode",
			args:  []string{"--sandbox-mode", "jail"},
			fails: true,
		},
		{
			name: "approval never",
			args: []string{"--approval", "never"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Approval != ApprovalNever {
					t.Fatalf("approval = %q", o.Approval)
				}
			},
		},
		{
			name:  "invalid approval",
			args:  []string{"--approval", "sometimes"},
			fails: true,
		},
		{
			name: "session and resume",
			args: []string{"--session", "abc", "--resume"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Session != "abc" || !o.Resume {
					t.Fatalf("session=%q resume=%v", o.Session, o.Resume)
				}
			},
		},
		{
			name: "locale",
			args: []string{"--locale", "zh-CN"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Locale != "zh-CN" {
					t.Fatalf("locale = %q", o.Locale)
				}
			},
		},
		{
			name: "config override wins over default",
			args: []string{"-c", "model=fast"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Model != "fast" {
					t.Fatalf("model = %q", o.Model)
				}
			},
		},
		{
			name: "dotted override nests",
			args: []string{"--config", `tools.include=["read"]`},
			want: func(t *testing.T, o *Options, _ []string) {
				tools, _ := o.Overrides.Nested()["tools"].(map[string]any)
				include, _ := tools["include"].([]any)
				if len(include) != 1 || include[0] != "read" {
					t.Fatalf("nested = %v", o.Overrides.Nested())
				}
			},
		},
		{
			name:  "override without equals",
			args:  []string{"-c", "model"},
			fails: true,
		},
		{
			name: "serve flags",
			args: []string{"--serve", "--serve-address", "127.0.0.1", "--serve-port", "9090", "--serve-token", "tok"},
			want: func(t *testing.T, o *Options, _ []string) {
				if !o.Serve || o.ServeAddr != "127.0.0.1:9090" || o.ServeToken != "tok" {
					t.Fatalf("serve=%v addr=%q token=%q", o.Serve, o.ServeAddr, o.ServeToken)
				}
			},
		},
		{
			name: "serve token from env",
			args: []string{"--serve"},
			env:  map[string]string{"ARGENT_SERVE_TOKEN": "envtok"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.ServeToken != "envtok" {
					t.Fatalf("token = %q", o.ServeToken)
				}
			},
		},
		{
			name: "rpc and acp",
			args: []string{"--rpc"},
			want: func(t *testing.T, o *Options, _ []string) {
				if !o.RPC {
					t.Fatal("rpc not set")
				}
			},
		},
		{
			name: "version",
			args: []string{"--version"},
			want: func(t *testing.T, o *Options, _ []string) {
				if !o.Version {
					t.Fatal("version not set")
				}
			},
		},
		{
			name: "help short circuits",
			args: []string{"-h"},
			want: func(t *testing.T, o *Options, _ []string) {
				if !o.Help {
					t.Fatal("help not set")
				}
			},
		},
		{
			name:  "unknown flag",
			args:  []string{"--nope"},
			want:  nil,
			fails: true,
		},
		{
			name: "terminator keeps dashes as prompt",
			args: []string{"--", "-m"},
			want: func(t *testing.T, o *Options, pos []string) {
				if len(pos) != 1 || pos[0] != "-m" {
					t.Fatalf("positional = %v", pos)
				}
				if o.Model != "gpt-4o-mini" {
					t.Fatalf("model = %q", o.Model)
				}
			},
		},
		{
			name: "env model with flag override",
			args: []string{"-m", "flag-m"},
			env:  map[string]string{"ARGENT_MODEL": "env-m"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Model != "flag-m" {
					t.Fatalf("model = %q", o.Model)
				}
			},
		},
		{
			name: "env beats config override",
			args: []string{"-c", "model=override-m"},
			env:  map[string]string{"ARGENT_MODEL": "env-m"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.Model != "env-m" {
					t.Fatalf("model = %q", o.Model)
				}
			},
		},
		{
			name: "env beats override for max turns",
			args: []string{"-c", "maxTurns=7"},
			env:  map[string]string{"ARGENT_MAX_TURNS": "5"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.MaxTurns != 5 {
					t.Fatalf("maxTurns = %d", o.MaxTurns)
				}
			},
		},
		{
			name: "flag beats env for max turns",
			args: []string{"--max-turns", "9"},
			env:  map[string]string{"ARGENT_MAX_TURNS": "5", "ARGENT_MODEL": "env-m"},
			want: func(t *testing.T, o *Options, _ []string) {
				if o.MaxTurns != 9 {
					t.Fatalf("maxTurns = %d", o.MaxTurns)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, positional, err := parseArgs(tc.args, func(key string) string { return tc.env[key] }, io.Discard)
			if tc.fails {
				if err == nil {
					t.Fatalf("expected error, got opts %+v", opts)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			tc.want(t, opts, positional)
		})
	}
}

func TestOverridesSetValidation(t *testing.T) {
	var overrides Overrides
	if err := overrides.Set("a.b=1"); err != nil {
		t.Fatalf("valid override rejected: %v", err)
	}
	if err := overrides.Set("=1"); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := overrides.Set("a..b=1"); err == nil {
		t.Fatal("empty segment accepted")
	}
	if err := overrides.Set("a="); err == nil {
		t.Fatal("empty value accepted")
	}
	if got := overrides.String(); got != "a.b=1" {
		t.Fatalf("String() = %q", got)
	}
}

func TestParseEvalFile(t *testing.T) {
	array := []byte(`[{"prompt":"a","expect_tool":"read","expect_text":"done"}]`)
	scenarios, err := parseEvalFile(array)
	if err != nil || len(scenarios) != 1 || scenarios[0].ExpectTool != "read" {
		t.Fatalf("array = %v, err %v", scenarios, err)
	}
	document := []byte(`{"scenarios":[{"prompt":"b"}]}`)
	scenarios, err = parseEvalFile(document)
	if err != nil || len(scenarios) != 1 || scenarios[0].Prompt != "b" {
		t.Fatalf("document = %v, err %v", scenarios, err)
	}
	if _, err := parseEvalFile([]byte(`{"nope":1}`)); err == nil {
		t.Fatal("object without scenarios accepted")
	}
}
