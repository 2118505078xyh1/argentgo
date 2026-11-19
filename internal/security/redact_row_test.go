package security

import (
	"bytes"
	"reflect"
	"regexp"
	"testing"
)

var ansiSequencePattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func ansiSequences(b []byte) []string {
	matches := ansiSequencePattern.FindAllString(string(b), -1)
	if matches == nil {
		return []string{}
	}
	return matches
}

func TestRedactRowPreservesANSISequences(t *testing.T) {
	line := []byte("\x1b[32mOK\x1b[0m api_key=sk-ant-abcdef123456 \x1b[1;4mdone\x1b[0m")
	before := ansiSequences(line)

	out := RedactRow(line)
	if bytes.Contains(out, []byte("sk-ant-abcdef123456")) {
		t.Fatalf("credential leaked into redacted row: %q", out)
	}
	if !bytes.Contains(out, []byte("[redacted:api key]")) {
		t.Fatalf("credential was not replaced: %q", out)
	}
	if got := ansiSequences(out); !reflect.DeepEqual(got, before) {
		t.Fatalf("ANSI sequences changed:\n got %q\nwant %q", got, before)
	}
}

func TestRedactRowRedactsEveryCredentialOnColoredLine(t *testing.T) {
	line := []byte("\x1b[31msk-proj-aaa111\x1b[0m and ghp_bbb222 \x1b[36mok\x1b[0m")
	before := ansiSequences(line)

	out := RedactRow(line)
	for _, secret := range []string{"sk-proj-aaa111", "ghp_bbb222"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Fatalf("credential %q leaked into redacted row: %q", secret, out)
		}
	}
	if got := ansiSequences(out); !reflect.DeepEqual(got, before) {
		t.Fatalf("ANSI sequences changed:\n got %q\nwant %q", got, before)
	}
}

func TestRedactRowLeavesCleanLineUnchanged(t *testing.T) {
	line := []byte("\x1b[33mwarning\x1b[0m: file src/main.go read OK")
	if got := RedactRow(line); !bytes.Equal(got, line) {
		t.Fatalf("RedactRow() = %q, want unchanged %q", got, line)
	}
}
