package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRedactDoctorDetailStripsURLUserinfo(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		mustLose  []string
		mustKeep  []string
		wantEqual string
	}{
		{
			name:     "https userinfo",
			in:       "https://user:secret@api.example.com/v1",
			mustLose: []string{"user", "secret"},
			mustKeep: []string{"https://api.example.com/v1"},
		},
		{
			name:     "userinfo in an unreachable error",
			in:       "https://admin:s3cr3t@api.example.com:8443/v1 unreachable: connection refused",
			mustLose: []string{"admin", "s3cr3t"},
			mustKeep: []string{"https://api.example.com:8443/v1 unreachable: connection refused"},
		},
		{
			name:     "non-http scheme and query",
			in:       "postgres://admin:pw@db.internal:5432/app?ssl=true",
			mustLose: []string{"admin", "pw"},
			mustKeep: []string{"postgres://db.internal:5432/app?ssl=true"},
		},
		{
			name:     "percent encoded password",
			in:       "https://user:p%40ss%3Aword@host.example/path",
			mustLose: []string{"p%40ss%3Aword"},
			mustKeep: []string{"https://host.example/path"},
		},
		{
			name:     "unescaped at sign in password",
			in:       "https://user:p@ss@host.example/x",
			mustLose: []string{"user:p@ss"},
			mustKeep: []string{"https://host.example/x"},
		},
		{
			name:     "quoted URL in an error string",
			in:       `Get "https://user:secret@api.example.com/v1": dial tcp: refused`,
			mustLose: []string{"user:secret"},
			mustKeep: []string{`https://api.example.com/v1": dial tcp: refused`},
		},
		{
			name:     "several URLs in one detail",
			in:       "both https://a:one@one.example and https://b:two@two.example failed",
			mustLose: []string{"a:one", "b:two"},
			mustKeep: []string{"https://one.example", "https://two.example"},
		},
		{
			name:      "bare username is not a password",
			in:        "https://user@host.example/path",
			wantEqual: "https://user@host.example/path",
		},
		{
			name:      "at sign in path is not userinfo",
			in:        "https://example.com/already@public/path",
			wantEqual: "https://example.com/already@public/path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDoctorDetail(tc.in)
			if tc.wantEqual != "" && got != tc.wantEqual {
				t.Fatalf("redactDoctorDetail(%q) = %q, want %q", tc.in, got, tc.wantEqual)
			}
			for _, secret := range tc.mustLose {
				if strings.Contains(got, secret) {
					t.Errorf("redactDoctorDetail(%q) = %q, leaked %q", tc.in, got, secret)
				}
			}
			for _, keep := range tc.mustKeep {
				if !strings.Contains(got, keep) {
					t.Errorf("redactDoctorDetail(%q) = %q, lost %q", tc.in, got, keep)
				}
			}
		})
	}
}

func TestRedactDoctorDetailRedactsCredentialShapes(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		mustLose string
	}{
		{
			name:     "api key in error string",
			in:       `Get "https://api.example.com": bearer sk-proj-abcdef123456 failed`,
			mustLose: "sk-proj-abcdef123456",
		},
		{
			name:     "authorization header",
			in:       "request rejected: Authorization: Basic dXNlcjpwYXNz",
			mustLose: "dXNlcjpwYXNz",
		},
		{
			name:     "key value pair",
			in:       "config error near api_key=AKIAIOSFODNN7EXAMPLE",
			mustLose: "AKIAIOSFODNN7EXAMPLE",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDoctorDetail(tc.in)
			if strings.Contains(got, tc.mustLose) {
				t.Fatalf("redactDoctorDetail(%q) = %q, leaked %q", tc.in, got, tc.mustLose)
			}
			if !strings.Contains(got, "[redacted:") {
				t.Fatalf("redactDoctorDetail(%q) = %q, want a redaction marker", tc.in, got)
			}
		})
	}
}

func TestRedactDoctorDetailLeavesCleanDetailsByteIdentical(t *testing.T) {
	cases := []string{
		"",
		`C:\Users\xyh\.argent\config.json`,
		"/home/u/.argent/config.json (defaults)",
		"go1.27.0",
		`C:\Program Files\Git\cmd\git.exe`,
		"https://api.openai.com/v1 returned HTTP 200",
		"http://127.0.0.1:1 unreachable: dial tcp 127.0.0.1:1: connectex refused",
		"no servers configured",
		"1 server(s) valid",
		"1 problem(s): broken: missing command and url",
		"mail user@example.com when the build finishes",
		"文献服务 1 server(s) valid",
		"配置正常，存储目录可写",
	}
	for _, in := range cases {
		if got := redactDoctorDetail(in); got != in {
			t.Errorf("redactDoctorDetail(%q) = %q, want unchanged", in, got)
		}
	}
}

func TestRedactDoctorDetailKeepsCJKIntact(t *testing.T) {
	in := "配置错误：无法连接 https://用户:密码@api.example.com/路径 结束"
	got := redactDoctorDetail(in)
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
	for _, secret := range []string{"用户:密码"} {
		if strings.Contains(got, secret) {
			t.Errorf("output leaked %q: %s", secret, got)
		}
	}
	for _, keep := range []string{"配置错误：无法连接", "https://api.example.com/路径", "结束"} {
		if !strings.Contains(got, keep) {
			t.Errorf("output lost %q: %s", keep, got)
		}
	}
}

func TestRedactDoctorDetailTruncatesLongDetails(t *testing.T) {
	// Just below, at and just above the limit.
	for _, n := range []int{doctorDetailMaxRunes - 1, doctorDetailMaxRunes} {
		in := strings.Repeat("x", n)
		if got := redactDoctorDetail(in); got != in {
			t.Fatalf("detail of %d runes was modified: %q", n, got)
		}
	}
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"ascii", strings.Repeat("x", doctorDetailMaxRunes+100)},
		{"cjk", strings.Repeat("中", doctorDetailMaxRunes+100)},
		{"mixed", strings.Repeat("a中", doctorDetailMaxRunes+100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDoctorDetail(tc.in)
			if !utf8.ValidString(got) {
				t.Fatal("truncated output is not valid UTF-8")
			}
			if !strings.HasSuffix(got, doctorDetailTruncated) {
				t.Fatalf("missing truncation marker: %q", got)
			}
			if n := utf8.RuneCountInString(got); n != doctorDetailMaxRunes+utf8.RuneCountInString(doctorDetailTruncated) {
				t.Fatalf("truncated detail has %d runes, want %d", n, doctorDetailMaxRunes+utf8.RuneCountInString(doctorDetailTruncated))
			}
		})
	}
}

func TestDoctorOutputRedactsCredentialInBaseURL(t *testing.T) {
	// The provider check fails immediately on a closed loopback port; its
	// detail still quotes the configured base URL, which must be scrubbed.
	const secret = "user:supersecret"
	baseURL := "https://" + secret + "@127.0.0.1:1/v1"

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ioState, out, _ := newTestIO("", nil)
	app := App{IO: ioState, StorageDir: dir, ConfigPath: path}
	RunWith(app, []string{"doctor", "--base-url", baseURL})

	output := out.String()
	if strings.Contains(output, secret) || strings.Contains(output, "supersecret") {
		t.Fatalf("doctor output leaked credentials:\n%s", output)
	}
	if !strings.Contains(output, "https://127.0.0.1:1/v1") {
		t.Fatalf("doctor output lost the provider host:\n%s", output)
	}
}
