package update

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const releaseJSON = `{
  "tag_name": "v0.5.0",
  "name": "argent 0.5.0",
  "body": "notes here",
  "html_url": "https://github.com/example/argent/releases/tag/v0.5.0",
  "published_at": "2026-11-01T10:00:00Z",
  "assets": [
    {"name": "argent_0.5.0_windows_amd64.zip", "browser_download_url": "https://example.invalid/win.zip", "size": 1234},
    {"name": "argent_0.5.0_linux_amd64.tar.gz", "browser_download_url": "https://example.invalid/linux.tar.gz", "size": 4321},
    {"name": "sha256sums.txt", "browser_download_url": "https://example.invalid/sums.txt", "size": 100}
  ]
}`

func releaseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/repos/example/argent/releases/latest") {
			t.Errorf("unexpected path %s", request.URL.Path)
		}
		if agent := request.Header.Get("User-Agent"); agent == "" {
			t.Error("request has no User-Agent")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		io.WriteString(writer, body)
	}))
	t.Cleanup(server.Close)
	original := apiBase
	apiBase = server.URL
	t.Cleanup(func() { apiBase = original })
	return server
}

func TestCheckParsesTheRelease(t *testing.T) {
	server := releaseServer(t, http.StatusOK, releaseJSON)
	release, newer, err := Check(context.Background(), "example/argent", "0.4.0", server.Client())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !newer {
		t.Fatal("0.5.0 should be newer than 0.4.0")
	}
	if release.Tag != "v0.5.0" || release.Version() != "0.5.0" {
		t.Fatalf("tag = %q, version = %q", release.Tag, release.Version())
	}
	if release.Repo != "example/argent" {
		t.Fatalf("repo = %q", release.Repo)
	}
	if release.Body != "notes here" || release.URL == "" {
		t.Fatalf("release body/url missing: %+v", release)
	}
	if len(release.Assets) != 3 {
		t.Fatalf("assets = %+v", release.Assets)
	}
	asset := release.Assets[0]
	if asset.Name != "argent_0.5.0_windows_amd64.zip" ||
		asset.URL != "https://example.invalid/win.zip" || asset.Size != 1234 {
		t.Fatalf("asset mapping = %+v", asset)
	}
	if release.PublishedAt.IsZero() {
		t.Fatal("published_at was not parsed")
	}
}

func TestCheckReportsNoUpdate(t *testing.T) {
	server := releaseServer(t, http.StatusOK, releaseJSON)
	for _, current := range []string{"0.5.0", "v0.5.0", "0.6.0", "1.0.0"} {
		_, newer, err := Check(context.Background(), "example/argent", current, server.Client())
		if err != nil {
			t.Fatalf("Check(%q): %v", current, err)
		}
		if newer {
			t.Fatalf("Check(%q) reported an update", current)
		}
	}
}

func TestCheckReportsAMissingRelease(t *testing.T) {
	server := releaseServer(t, http.StatusNotFound, `{"message":"Not Found"}`)
	_, newer, err := Check(context.Background(), "example/argent", "1.0.0", server.Client())
	if err == nil {
		t.Fatal("Check succeeded on 404")
	}
	if newer {
		t.Fatal("404 must not report an update")
	}
	if !strings.Contains(err.Error(), "no published releases") {
		t.Fatalf("error = %v", err)
	}
}

func TestCheckReportsServerErrors(t *testing.T) {
	server := releaseServer(t, http.StatusInternalServerError, "boom")
	if _, _, err := Check(context.Background(), "example/argent", "1.0.0", server.Client()); err == nil {
		t.Fatal("Check succeeded on 500")
	}
}

func TestCheckRejectsMalformedRepo(t *testing.T) {
	for _, repo := range []string{"", "argent", "a/b/c", " /x", "x/ ", "a b/c"} {
		if _, _, err := Check(context.Background(), repo, "1.0.0", nil); err == nil {
			t.Fatalf("Check(%q) succeeded, want an error", repo)
		}
	}
}

func TestCheckRejectsAReleaseWithoutATag(t *testing.T) {
	server := releaseServer(t, http.StatusOK, `{"name":"no tag"}`)
	if _, _, err := Check(context.Background(), "example/argent", "1.0.0", server.Client()); err == nil {
		t.Fatal("Check accepted a release without tag_name")
	}
}
