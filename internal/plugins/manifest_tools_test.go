package plugins

import "testing"

func TestValidateManifestUnsupportedTools(t *testing.T) {
	m := Manifest{Name: "p", Version: "1.0.0", Tools: []string{"read", "write"}}
	issues := ValidateManifest(m)
	found := false
	for _, issue := range issues {
		if issue.Kind == IssueUnsupportedField && issue.Field == "tools" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no unsupported-field issue for tools: %+v", issues)
	}
}

func TestValidateManifestNoToolsNoIssue(t *testing.T) {
	m := Manifest{Name: "p", Version: "1.0.0"}
	for _, issue := range ValidateManifest(m) {
		if issue.Kind == IssueUnsupportedField {
			t.Fatalf("unexpected unsupported-field issue: %+v", issue)
		}
	}
}
