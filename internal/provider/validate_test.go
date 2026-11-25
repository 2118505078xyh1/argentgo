package provider_test

import (
	"strings"
	"sync"
	"testing"

	"example.com/argentgo/internal/provider"
)

// diagnosticsFields 返回携带给定严重性的 Field 集合。
func diagnosticsFields(diagnostics []provider.Diagnostic, severity provider.Severity) map[string]bool {
	fields := make(map[string]bool)
	for _, d := range diagnostics {
		if d.Severity == severity {
			fields[d.Field] = true
		}
	}
	return fields
}

func TestValidateBuiltInCatalogIsClean(t *testing.T) {
	diagnostics := provider.ValidateAll()
	if len(diagnostics) != 0 {
		for _, d := range diagnostics {
			t.Logf("diagnostic: %s", d)
		}
		t.Fatalf("built-in catalog has %d diagnostics, want 0", len(diagnostics))
	}

	// 每个内置模板还必须独立成立，不依赖
	// 每个内置模板还必须独立成立，不依赖 ValidateAll 执行的别名交叉检查。
	for _, template := range provider.List() {
		if issues := provider.Validate(template); len(issues) != 0 {
			t.Errorf("Validate(%q) = %v, want clean", template.Name, issues)
		}
	}
}

func TestValidateReportsMissingRequiredFields(t *testing.T) {
	diagnostics := provider.Validate(provider.Template{})
	errors := diagnosticsFields(diagnostics, provider.SeverityError)
	for _, field := range []string{"Name", "Protocol", "BaseURL", "DefaultModel"} {
		if !errors[field] {
			t.Errorf("zero template is missing an %s error; got %v", field, diagnostics)
		}
	}
	if !provider.HasErrors(diagnostics) {
		t.Fatal("HasErrors = false for a template with four errors")
	}
	if provider.HasErrors(provider.Validate(provider.Template{
		Name:         "custom",
		Protocol:     provider.ProtocolOpenAI,
		BaseURL:      "https://example.test/v1",
		DefaultModel: "m",
		APIKeyEnv:    "CUSTOM_KEY",
	})) {
		t.Fatal("HasErrors = true for a sound template")
	}
}

func TestValidateRejectsMalformedEndpointURLs(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		wantErr  bool
		wantWarn bool
	}{
		{"valid https", "https://api.example.test/v1", false, false},
		{"valid http loopback", "http://127.0.0.1:11434/v1", false, false},
		{"no scheme", "api.example.test/v1", true, false},
		{"unsupported scheme", "ftp://api.example.test/v1", true, false},
		{"missing host", "https:///v1", true, false},
		{"embedded credentials", "https://user:pass@api.example.test/v1", true, false},
		{"query string", "https://api.example.test/v1?key=abc", true, false},
		{"fragment", "https://api.example.test/v1#frag", true, false},
		{"path traversal", "https://api.example.test/../v1", true, false},
		{"trailing slash", "https://api.example.test/v1/", false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			template := provider.Template{
				Name:         "custom",
				Protocol:     provider.ProtocolOpenAI,
				BaseURL:      tc.baseURL,
				DefaultModel: "m",
				APIKeyEnv:    "CUSTOM_KEY",
			}
			diagnostics := provider.Validate(template)
			errors := diagnosticsFields(diagnostics, provider.SeverityError)
			warnings := diagnosticsFields(diagnostics, provider.SeverityWarning)
			if errors["BaseURL"] != tc.wantErr {
				t.Errorf("BaseURL error = %v, want %v (diagnostics: %v)", errors["BaseURL"], tc.wantErr, diagnostics)
			}
			if warnings["BaseURL"] != tc.wantWarn {
				t.Errorf("BaseURL warning = %v, want %v (diagnostics: %v)", warnings["BaseURL"], tc.wantWarn, diagnostics)
			}
		})
	}
}

func TestValidateFlagsCredentialInconsistencies(t *testing.T) {
	cases := []struct {
		name     string
		template provider.Template
		wantErr  string
		wantWarn string
	}{
		{
			name: "NoAuth with declared keys warns",
			template: provider.Template{
				Name: "custom", Protocol: provider.ProtocolOpenAI,
				BaseURL: "https://api.example.test/v1", DefaultModel: "m",
				NoAuth: true, APIKeyEnv: "CUSTOM_KEY",
			},
			wantWarn: "NoAuth",
		},
		{
			name: "invalid env name is an error",
			template: provider.Template{
				Name: "custom", Protocol: provider.ProtocolOpenAI,
				BaseURL: "https://api.example.test/v1", DefaultModel: "m",
				APIKeyEnvs: []string{"BAD=NAME"},
			},
			wantErr: "APIKeyEnv",
		},
		{
			name: "duplicate credential sources warn",
			template: provider.Template{
				Name: "custom", Protocol: provider.ProtocolOpenAI,
				BaseURL: "https://api.example.test/v1", DefaultModel: "m",
				APIKeyEnv: "CUSTOM_KEY", APIKeyEnvs: []string{"CUSTOM_KEY"},
			},
			wantWarn: "APIKeyEnvs",
		},
		{
			name: "remote endpoint without credentials warns",
			template: provider.Template{
				Name: "custom", Protocol: provider.ProtocolOpenAI,
				BaseURL: "https://api.example.test/v1", DefaultModel: "m",
			},
			wantWarn: "APIKeyEnv",
		},
		{
			name: "loopback endpoint without credentials is clean",
			template: provider.Template{
				Name: "custom", Protocol: provider.ProtocolOpenAI,
				BaseURL: "http://localhost:1234/v1", DefaultModel: "m",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostics := provider.Validate(tc.template)
			errors := diagnosticsFields(diagnostics, provider.SeverityError)
			warnings := diagnosticsFields(diagnostics, provider.SeverityWarning)
			if errors[tc.wantErr] != (tc.wantErr != "") {
				t.Errorf("want error field %q, got %v", tc.wantErr, diagnostics)
			}
			if warnings[tc.wantWarn] != (tc.wantWarn != "") {
				t.Errorf("want warning field %q, got %v", tc.wantWarn, diagnostics)
			}
		})
	}
}

func TestValidateFlagsProtocolURLMismatch(t *testing.T) {
	cases := []struct {
		name     string
		protocol provider.Protocol
		baseURL  string
		wantWarn bool
	}{
		{"openai /v1", provider.ProtocolOpenAI, "https://api.example.test/v1", false},
		{"openai /v2 warns", provider.ProtocolOpenAI, "https://api.example.test/v2", true},
		{"responses /v1", provider.ProtocolResponses, "https://api.example.test/v1", false},
		{"anthropic native /v1", provider.ProtocolAnthropic, "https://api.anthropic.com/v1", false},
		{"anthropic compatible /anthropic", provider.ProtocolAnthropic, "https://api.example.test/anthropic", false},
		{"anthropic /api/anthropic", provider.ProtocolAnthropic, "https://api.example.test/api/anthropic", false},
		{"anthropic /v2 warns", provider.ProtocolAnthropic, "https://api.example.test/v2", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			template := provider.Template{
				Name: "custom", Protocol: tc.protocol,
				BaseURL: tc.baseURL, DefaultModel: "m",
				APIKeyEnv: "CUSTOM_KEY",
			}
			warnings := diagnosticsFields(provider.Validate(template), provider.SeverityWarning)
			if warnings["BaseURL"] != tc.wantWarn {
				t.Errorf("BaseURL warning = %v, want %v", warnings["BaseURL"], tc.wantWarn)
			}
		})
	}
}

func TestValidateCatalogDetectsDuplicateNames(t *testing.T) {
	template := provider.Template{
		Protocol:     provider.ProtocolOpenAI,
		BaseURL:      "https://api.example.test/v1",
		DefaultModel: "m",
		APIKeyEnv:    "CUSTOM_KEY",
	}
	template.Name = "same"
	other := template

	diagnostics := provider.ValidateCatalog([]provider.Template{template, other})
	duplicates := 0
	for _, d := range diagnostics {
		if d.Severity == provider.SeverityError && d.Field == "Name" && strings.Contains(d.Message, "duplicate") {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("duplicate name findings = %d, want 1 (diagnostics: %v)", duplicates, diagnostics)
	}

	// 验证单个模板无法看到跨模板重复。
	if issues := provider.Validate(template); len(issues) != 0 {
		t.Fatalf("single Validate should be clean: %v", issues)
	}
}

func TestDiagnosticStringAndValidationPurity(t *testing.T) {
	d := provider.Diagnostic{
		Severity: provider.SeverityWarning,
		Template: "openai",
		Field:    "BaseURL",
		Message:  "trailing slash",
	}
	if got, want := d.String(), "[warning] openai/BaseURL: trailing slash"; got != want {
		t.Fatalf("Diagnostic.String() = %q, want %q", got, want)
	}
	bare := provider.Diagnostic{Severity: provider.SeverityError, Message: "boom"}
	if got, want := bare.String(), "[error] boom"; got != want {
		t.Fatalf("bare Diagnostic.String() = %q, want %q", got, want)
	}

	template := provider.Template{
		Name: "custom", Protocol: provider.ProtocolOpenAI,
		BaseURL: "https://api.example.test/v1/", DefaultModel: "m",
		APIKeyEnv: "CUSTOM_KEY",
	}
	want := provider.Validate(template)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if got := provider.Validate(template); len(got) != len(want) {
					errs <- "Validate gave an inconsistent number of findings"
					return
				}
				if provider.ValidateAll() == nil {
					errs <- "ValidateAll returned nil"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if template.BaseURL != "https://api.example.test/v1/" {
		t.Fatal("Validate mutated its input")
	}
}
