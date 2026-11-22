package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTTSRRules(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(good, []byte(`[
		{"name":"stop","scope":"text","pattern":"STOP","action":"retry","reminder":"do not stop"},
		{"name":"warn","scope":"text","pattern":"WARN","action":"warn","reminder":"careful"}
	]`), 0644); err != nil {
		t.Fatal(err)
	}
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte(`{not json`), 0644); err != nil {
		t.Fatal(err)
	}
	badRegex := filepath.Join(dir, "badregex.json")
	if err := os.WriteFile(badRegex, []byte(`[
		{"name":"ok","scope":"text","pattern":"OK","action":"warn","reminder":"x"},
		{"name":"broken","scope":"text","pattern":"[","action":"warn","reminder":"x"}
	]`), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		value   string
		wantNil bool
		wantLen int
		wantErr bool
	}{
		{"unset", "", true, 0, false},
		{"good relative", "rules.json", false, 2, false},
		{"good absolute", good, false, 2, false},
		{"missing file", "nope.json", true, 0, true},
		{"bad json", badJSON, true, 0, true},
		{"bad regex skipped", badRegex, false, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := func(k string) string {
				if k == ttsrRulesEnv {
					return tt.value
				}
				return ""
			}
			rules, err := loadTTSRRules(env, dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantNil {
				if rules != nil {
					t.Fatalf("want nil rules, got %d", len(rules))
				}
				return
			}
			if len(rules) != tt.wantLen {
				t.Fatalf("rules=%d want %d", len(rules), tt.wantLen)
			}
		})
	}
}

func TestTTSRRulesForRunWarnsOnFailure(t *testing.T) {
	var warned []string
	env := func(k string) string {
		if k == ttsrRulesEnv {
			return "missing.json"
		}
		return ""
	}
	rules := ttsrRulesForRun(env, t.TempDir(), func(msg string) { warned = append(warned, msg) })
	if rules != nil {
		t.Fatalf("want nil rules on load failure, got %d", len(rules))
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "TTSR 规则加载失败") {
		t.Fatalf("warn=%q", warned)
	}
}
