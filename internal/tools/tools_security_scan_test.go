package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const scanTestAWSKey = "AKIAIOSFODNN7EXAMPLE"

func writeScanFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runSecurityScan(t *testing.T, root, args string) (securityScanResult, string) {
	t.Helper()
	out, err := (securityScanTool{}).Run(context.Background(), root, json.RawMessage(args))
	if err != nil {
		t.Fatalf("security_scan %s: %v", args, err)
	}
	var result securityScanResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid result JSON %q: %v", out, err)
	}
	return result, out
}

func TestSecurityScanFindsCredentialsAndRedactsSnippets(t *testing.T) {
	root := t.TempDir()
	github := "ghp_" + strings.Repeat("a", 36)
	writeScanFile(t, root, "src/keys.go",
		"package main\nvar aws = \""+scanTestAWSKey+"\"\nvar gh = \""+github+"\"\n")

	result, out := runSecurityScan(t, root, `{}`)
	if result.ScannedFiles != 1 {
		t.Fatalf("scanned_files = %d, want 1", result.ScannedFiles)
	}
	categories := make(map[string]bool)
	for _, finding := range result.Findings {
		categories[finding.Category] = true
		if finding.File != "src/keys.go" || finding.Line < 2 {
			t.Fatalf("finding = %+v, want src/keys.go at line >= 2", finding)
		}
	}
	if !categories["aws_access_key"] || !categories["github_token"] {
		t.Fatalf("categories = %v, want aws_access_key and github_token", categories)
	}
	if strings.Contains(out, scanTestAWSKey) || strings.Contains(out, github) {
		t.Fatalf("raw credential leaked into output: %s", out)
	}
}

func TestSecurityScanCleanFileHasNoFindings(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, "clean.go", "package main\nfunc main() { println(\"hello\") }\n")

	result, _ := runSecurityScan(t, root, `{}`)
	if result.ScannedFiles != 1 || len(result.Findings) != 0 {
		t.Fatalf("result = %+v, want 1 scanned file and no findings", result)
	}
}

func TestSecurityScanSkipsVendoredAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, ".git/config", "key = "+scanTestAWSKey)
	writeScanFile(t, root, "node_modules/pkg/index.js", "const k = \""+scanTestAWSKey+"\";")
	writeScanFile(t, root, "vendor/lib.go", "var k = \""+scanTestAWSKey+"\"")
	binPath := writeScanFile(t, root, "blob.bin", "placeholder")
	if err := os.WriteFile(binPath, append([]byte{0x00, 0x01, 0x02}, []byte(scanTestAWSKey)...), 0o644); err != nil {
		t.Fatal(err)
	}
	writeScanFile(t, root, "src/ok.go", "var k = \""+scanTestAWSKey+"\"")

	result, out := runSecurityScan(t, root, `{}`)
	if result.ScannedFiles != 1 {
		t.Fatalf("scanned_files = %d, want 1 (vendored and binary files skipped)", result.ScannedFiles)
	}
	if len(result.Findings) != 1 || result.Findings[0].File != "src/ok.go" {
		t.Fatalf("findings = %+v, want only src/ok.go", result.Findings)
	}
	if strings.Contains(out, scanTestAWSKey) {
		t.Fatalf("raw credential leaked into output: %s", out)
	}
}

func TestSecurityScanRespectsMaxFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 6; i++ {
		writeScanFile(t, root, fmt.Sprintf("d%02d/f.go", i), "package p\n")
	}
	result, _ := runSecurityScan(t, root, `{"max_files":2}`)
	if result.ScannedFiles != 2 {
		t.Fatalf("scanned_files = %d, want 2", result.ScannedFiles)
	}
}

func TestSecurityScanPathRestrictsScope(t *testing.T) {
	root := t.TempDir()
	writeScanFile(t, root, "keep/a.go", "var k = \""+scanTestAWSKey+"\"")
	writeScanFile(t, root, "skip/b.go", "var k = \""+scanTestAWSKey+"\"")

	result, _ := runSecurityScan(t, root, `{"path":"keep"}`)
	if result.ScannedFiles != 1 {
		t.Fatalf("scanned_files = %d, want 1", result.ScannedFiles)
	}
	if len(result.Findings) != 1 || result.Findings[0].File != "a.go" {
		t.Fatalf("findings = %+v, want only a.go", result.Findings)
	}
}

func TestSecurityScanMetaSchemaAndOutputBound(t *testing.T) {
	tool := securityScanTool{}
	if tool.Mutating() {
		t.Fatal("security_scan must be read-only")
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(tool.Schema()), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object", schema["type"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	for _, name := range []string{"path", "max_files", "include"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("schema missing %q: %v", name, properties)
		}
	}

	root := t.TempDir()
	var content strings.Builder
	for i := 0; i < 500; i++ {
		content.WriteString("var k = \"" + scanTestAWSKey + "\"\n")
	}
	writeScanFile(t, root, "many.go", content.String())

	const limit = 1000
	ctx := context.WithValue(context.Background(), outputLimitKey{}, limit)
	out, err := tool.Run(ctx, root, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("security_scan: %v", err)
	}
	if len(out) > limit {
		t.Fatalf("output length %d exceeds limit %d", len(out), limit)
	}
	var result securityScanResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("truncated output is not valid JSON: %v", err)
	}
	if !result.Truncated || result.Marker == "" {
		t.Fatalf("result = %+v, want truncated with a marker", result)
	}
	if len(result.Findings) == 0 {
		t.Fatal("truncated result kept no findings")
	}
}

func TestSecurityScanRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	_, err := (securityScanTool{}).Run(context.Background(), root, json.RawMessage(`{"path":"../outside"}`))
	if err == nil || !strings.Contains(err.Error(), "outside the workspace") {
		t.Fatalf("err = %v, want an outside-workspace rejection", err)
	}
}

func TestSecurityScanFindingsSortedByFileAndLine(t *testing.T) {
	root := t.TempDir()
	secret := "ghp_" + strings.Repeat("b", 36)
	writeScanFile(t, root, "b.go", "var k = \""+secret+"\"\nvar j = \""+secret+"\"\n")
	writeScanFile(t, root, "a.go", "var k = \""+secret+"\"\n")

	result, _ := runSecurityScan(t, root, `{}`)
	for i := 1; i < len(result.Findings); i++ {
		prev, curr := result.Findings[i-1], result.Findings[i]
		if prev.File > curr.File || (prev.File == curr.File && prev.Line > curr.Line) {
			t.Fatalf("findings not sorted: %+v then %+v", prev, curr)
		}
	}
	if len(result.Findings) != 3 {
		t.Fatalf("findings = %+v, want 3", result.Findings)
	}
}
