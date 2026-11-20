package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestVersionEndpointReturnsStructuredJSON(t *testing.T) {
	h := NewHandler(Config{Token: "s3cret", Runner: &fakeRunner{}, Version: "9.9.9"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/version", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, want application/json", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode version body %q: %v", rec.Body.String(), err)
	}
	wantKeys := []string{"version", "commit", "build_id", "go_version", "platform"}
	if len(got) != len(wantKeys) {
		t.Fatalf("keys = %v, want exactly %v", got, wantKeys)
	}
	for _, key := range wantKeys {
		value, ok := got[key].(string)
		if !ok || value == "" {
			t.Fatalf("field %q = %v, want a non-empty string", key, got[key])
		}
	}
	if got["version"] != "9.9.9" {
		t.Fatalf("version = %v, want 9.9.9", got["version"])
	}
}

func TestVersionEndpointNeedsNoToken(t *testing.T) {
	h := NewHandler(Config{Token: "s3cret", Runner: &fakeRunner{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestVersionEndpointRejectsNonGet(t *testing.T) {
	h := NewHandler(Config{Token: "t", Runner: &fakeRunner{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/version", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func TestVersionInfoHasCompleteRuntimeFields(t *testing.T) {
	originalCommit, originalBuildID := Commit, BuildID
	Commit, BuildID = "abc123", "build-42"
	t.Cleanup(func() { Commit, BuildID = originalCommit, originalBuildID })

	got := newVersionInfo("1.2.3")
	want := VersionInfo{
		Version:   "1.2.3",
		Commit:    "abc123",
		BuildID:   "build-42",
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	if got != want {
		t.Fatalf("version info = %+v, want %+v", got, want)
	}
}

func TestVersionInfoDefaultsMissingMetadata(t *testing.T) {
	originalCommit, originalBuildID := Commit, BuildID
	Commit, BuildID = "  ", ""
	t.Cleanup(func() { Commit, BuildID = originalCommit, originalBuildID })

	got := newVersionInfo("  ")
	if got.Version != defaultVersion {
		t.Fatalf("version = %q, want %q", got.Version, defaultVersion)
	}
	if got.BuildID != unknownBuildMeta {
		t.Fatalf("build id = %q, want %q", got.BuildID, unknownBuildMeta)
	}
	if strings.TrimSpace(got.Commit) == "" {
		t.Fatal("commit is empty")
	}
	if got.GoVersion == "" || got.Platform == "" {
		t.Fatalf("runtime fields are empty: %+v", got)
	}
}
