package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectMCPConfig(t *testing.T) {
	obj := func(entries map[string]any) map[string]any { return entries }
	tests := []struct {
		name         string
		raw          map[string]any
		wantServers  int
		wantShapeOK  bool
		wantContains []string
		wantProblems bool
	}{
		{
			name:        "nil config",
			raw:         nil,
			wantShapeOK: true,
		},
		{
			name:        "empty config",
			raw:         map[string]any{},
			wantShapeOK: true,
		},
		{
			name:        "mcpServers absent",
			raw:         obj(map[string]any{"model": "gpt-4o"}),
			wantShapeOK: true,
		},
		{
			name:        "empty mcpServers object",
			raw:         obj(map[string]any{"mcpServers": map[string]any{}}),
			wantShapeOK: true,
		},
		{
			name: "valid stdio server",
			raw: obj(map[string]any{"mcpServers": map[string]any{
				"fs": map[string]any{"command": "npx", "args": []any{"-y", "srv"}},
			}}),
			wantServers: 1,
			wantShapeOK: true,
		},
		{
			name: "valid http server with CJK name",
			raw: obj(map[string]any{"mcpServers": map[string]any{
				"文献服务": map[string]any{"url": "https://example.test/mcp"},
			}}),
			wantServers: 1,
			wantShapeOK: true,
		},
		{
			name: "list form with valid servers",
			raw: obj(map[string]any{"mcpServers": []any{
				map[string]any{"name": "one", "command": "srv"},
				map[string]any{"name": "two", "url": "https://example.test"},
			}}),
			wantServers: 2,
			wantShapeOK: true,
		},
		{
			name: "server missing target",
			raw: obj(map[string]any{"mcpServers": map[string]any{
				"broken": map[string]any{},
			}}),
			wantServers:  1,
			wantShapeOK:  true,
			wantProblems: true,
			wantContains: []string{"broken", "missing command and url"},
		},
		{
			name: "CJK server missing target",
			raw: obj(map[string]any{"mcpServers": map[string]any{
				"知识库": map[string]any{"transport": "http"},
			}}),
			wantServers:  1,
			wantShapeOK:  true,
			wantProblems: true,
			wantContains: []string{"知识库", "missing command and url"},
		},
		{
			name: "bad field type reports problem",
			raw: obj(map[string]any{"mcpServers": map[string]any{
				"bad": map[string]any{"command": float64(42)},
			}}),
			wantServers:  1,
			wantShapeOK:  true,
			wantProblems: true,
			wantContains: []string{"bad", "want a string", "missing command and url"},
		},
		{
			name: "unsupported transport",
			raw: obj(map[string]any{"mcpServers": map[string]any{
				"ws": map[string]any{"url": "wss://example.test", "transport": "websocket"},
			}}),
			wantServers:  1,
			wantShapeOK:  true,
			wantProblems: true,
			wantContains: []string{"ws", "transport", "unsupported transport", "websocket"},
		},
		{
			name: "disabled server exempt from reachability checks",
			raw: obj(map[string]any{"mcpServers": map[string]any{
				"off": map[string]any{"enabled": false},
			}}),
			wantServers: 1,
			wantShapeOK: true,
		},
		{
			name: "wrong shape string",
			raw:  obj(map[string]any{"mcpServers": "oops"}),
			// shapeOK false; a problem is still reported.
			wantProblems: true,
			wantContains: []string{"mcpServers must be an object or array"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := inspectMCPConfig(tt.raw)
			if report.shapeOK != tt.wantShapeOK {
				t.Fatalf("shapeOK = %v, want %v", report.shapeOK, tt.wantShapeOK)
			}
			if report.servers != tt.wantServers {
				t.Fatalf("servers = %d, want %d", report.servers, tt.wantServers)
			}
			joined := ""
			for _, problem := range report.problems {
				joined += problem.Error() + "\n"
			}
			if tt.wantProblems && len(report.problems) == 0 {
				t.Fatalf("expected problems, got none")
			}
			if !tt.wantProblems && len(report.problems) != 0 {
				t.Fatalf("unexpected problems: %s", joined)
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(joined, want) {
					t.Fatalf("problems %q missing %q", joined, want)
				}
			}
		})
	}
}

func TestCheckMCPConfigStatus(t *testing.T) {
	tests := []struct {
		name         string
		configJSON   string
		writeFile    bool
		wantStatus   string
		wantInDetail string
	}{
		{
			name:         "missing config file is clean",
			writeFile:    false,
			wantStatus:   statusOK,
			wantInDetail: "no servers configured",
		},
		{
			name:         "empty config is clean",
			configJSON:   `{}`,
			writeFile:    true,
			wantStatus:   statusOK,
			wantInDetail: "no servers configured",
		},
		{
			name:         "valid server is ok",
			configJSON:   `{"mcpServers":{"fs":{"command":"npx"}}}`,
			writeFile:    true,
			wantStatus:   statusOK,
			wantInDetail: "1 server(s) valid",
		},
		{
			name:         "CJK valid server is ok",
			configJSON:   `{"mcpServers":{"知识库":{"url":"https://example.test"}}}`,
			writeFile:    true,
			wantStatus:   statusOK,
			wantInDetail: "1 server(s) valid",
		},
		{
			name:         "missing target warns",
			configJSON:   `{"mcpServers":{"broken":{}}}`,
			writeFile:    true,
			wantStatus:   statusWarn,
			wantInDetail: "broken",
		},
		{
			name:         "CJK missing target warns with name",
			configJSON:   `{"mcpServers":{"知识库":{}}}`,
			writeFile:    true,
			wantStatus:   statusWarn,
			wantInDetail: "知识库",
		},
		{
			name:         "wrong shape fails",
			configJSON:   `{"mcpServers":123}`,
			writeFile:    true,
			wantStatus:   statusFail,
			wantInDetail: "mcpServers must be an object or array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if tt.writeFile {
				if err := os.WriteFile(path, []byte(tt.configJSON), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			check := checkMCPConfig(App{ConfigPath: path})
			if check.name != "mcp" {
				t.Fatalf("name = %q, want mcp", check.name)
			}
			if check.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (detail: %s)", check.status, tt.wantStatus, check.detail)
			}
			if !strings.Contains(check.detail, tt.wantInDetail) {
				t.Fatalf("detail %q missing %q", check.detail, tt.wantInDetail)
			}
		})
	}
}

func TestDoctorReportsMCPConfigIntegrity(t *testing.T) {
	// The provider check is not local, so point it at a loopback port that
	// refuses connections immediately; it becomes a warning and never changes
	// the exit code. Assertions below only concern the local mcp check.
	const refusedURL = "http://127.0.0.1:1"

	tests := []struct {
		name       string
		configJSON string
		wantExit   int
		wantLine   string
	}{
		{
			name:       "valid config passes doctor",
			configJSON: `{"mcpServers":{"fs":{"command":"npx"}}}`,
			wantExit:   0,
			wantLine:   "ok   mcp",
		},
		{
			name:       "broken mcp config fails doctor",
			configJSON: `{"mcpServers":"not-an-object"}`,
			wantExit:   1,
			wantLine:   "fail mcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte(tt.configJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			ioState, out, _ := newTestIO("", nil)
			app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
			code := RunWith(app, []string{"doctor", "--base-url", refusedURL})
			if code != tt.wantExit {
				t.Fatalf("exit = %d, want %d; output:\n%s", code, tt.wantExit, out.String())
			}
			if !strings.Contains(out.String(), tt.wantLine) {
				t.Fatalf("output missing %q:\n%s", tt.wantLine, out.String())
			}
		})
	}
}
