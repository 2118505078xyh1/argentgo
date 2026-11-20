package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDecodeResourceBlob(t *testing.T) {
	raw := []byte("hello \x00\xff world")
	tests := []struct {
		name     string
		resource ResourceContent
		want     []byte
		wantMIME string
		wantErr  error
	}{
		{
			name:     "padded std base64",
			resource: ResourceContent{MIMEType: "image/png", Blob: base64.StdEncoding.EncodeToString(raw)},
			want:     raw,
			wantMIME: "image/png",
		},
		{
			name:     "unpadded raw base64",
			resource: ResourceContent{MIMEType: "audio/wav", Blob: base64.RawStdEncoding.EncodeToString(raw)},
			want:     raw,
			wantMIME: "audio/wav",
		},
		{
			name:     "mime is trimmed",
			resource: ResourceContent{MIMEType: "  application/pdf  ", Blob: base64.StdEncoding.EncodeToString(raw)},
			want:     raw,
			wantMIME: "application/pdf",
		},
		{
			name:     "empty blob",
			resource: ResourceContent{MIMEType: "application/octet-stream"},
			wantErr:  ErrBlobEmpty,
		},
		{
			name:     "invalid base64",
			resource: ResourceContent{Blob: "!!!not base64!!!"},
			wantErr:  ErrBlobBadBase64,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, mime, err := DecodeResourceBlob(tt.resource)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeResourceBlob: %v", err)
			}
			if !bytes.Equal(data, tt.want) {
				t.Fatalf("data = %q, want %q", data, tt.want)
			}
			if mime != tt.wantMIME {
				t.Fatalf("mime = %q, want %q", mime, tt.wantMIME)
			}
		})
	}
}

func TestDecodeResourceBlobSizeLimit(t *testing.T) {
	// Cheap rejection: an encoded string longer than any 16 MiB payload can be
	// is refused before DecodeString allocates.
	tooLong := strings.Repeat("A", base64.StdEncoding.EncodedLen(maxResourceBlobBytes)+4+1)
	if _, _, err := DecodeResourceBlob(ResourceContent{Blob: tooLong}); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("oversized encoded length: err = %v, want ErrBlobTooLarge", err)
	}

	// A real payload one byte over the cap must also be rejected (post-decode).
	over := bytes.Repeat([]byte{0xAB}, maxResourceBlobBytes+1)
	encoded := base64.StdEncoding.EncodeToString(over)
	if _, _, err := DecodeResourceBlob(ResourceContent{Blob: encoded}); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("payload over cap: err = %v, want ErrBlobTooLarge", err)
	}

	// A payload exactly at the cap is accepted.
	atCap := bytes.Repeat([]byte{0xCD}, maxResourceBlobBytes)
	data, _, err := DecodeResourceBlob(ResourceContent{Blob: base64.StdEncoding.EncodeToString(atCap)})
	if err != nil {
		t.Fatalf("payload at cap: %v", err)
	}
	if len(data) != maxResourceBlobBytes {
		t.Fatalf("decoded length = %d, want %d", len(data), maxResourceBlobBytes)
	}
}

func TestSaveBlobArtifactRejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	bad := []string{
		"../../evil",
		"/etc/passwd",
		`..\..\evil`,
		"a/b",
		`a\b`,
		"..",
		".",
		"a.b",
		"-x",
		strings.Repeat("a", maxArtifactExtLen+1),
	}
	for _, ext := range bad {
		if _, err := SaveBlobArtifact(dir, []byte("x"), ext); err == nil {
			t.Errorf("ext %q: expected path-escape/invalid-ext rejection", ext)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil {
		t.Fatalf("read dir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("rejected extensions must not create files: %v", entries)
	}
	if _, err := SaveBlobArtifact("", []byte("x"), ".bin"); err == nil {
		t.Error("empty artifact directory must be rejected")
	}
}

func TestSaveBlobArtifact(t *testing.T) {
	dir := t.TempDir()
	data := []byte("binary\x00\xff\xfepayload")

	path, err := SaveBlobArtifact(dir, data, "bin")
	if err != nil {
		t.Fatalf("SaveBlobArtifact: %v", err)
	}
	if !pathWithinDir(dir, path) {
		t.Fatalf("artifact escaped dir: %s", path)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, blobArtifactPrefix) || !strings.HasSuffix(base, ".bin") {
		t.Fatalf("unexpected artifact name: %q", base)
	}
	if got, err := os.ReadFile(path); err != nil {
		t.Fatalf("read artifact: %v", err)
	} else if !bytes.Equal(got, data) {
		t.Fatalf("artifact content = %q, want %q", got, data)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat artifact: %v", err)
	} else if !info.Mode().IsRegular() {
		t.Fatalf("artifact is not a regular file: %v", info.Mode())
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		// Windows does not honour the requested permission bits (reports 0666).
		t.Fatalf("artifact mode = %o, want 600", info.Mode().Perm())
	}

	// An empty extension yields an extensionless artifact.
	path2, err := SaveBlobArtifact(dir, data, "")
	if err != nil {
		t.Fatalf("SaveBlobArtifact (no ext): %v", err)
	}
	if strings.Contains(filepath.Base(path2), ".") {
		t.Fatalf("empty ext should not add a dot: %q", path2)
	}
}

func TestCallToolBlobWithoutArtifactDirUnchanged(t *testing.T) {
	// Explicitly disabled: output must match the legacy parser byte-for-byte.
	t.Setenv(blobArtifactDirEnv, "")

	payload := []byte("secret binary")
	blob := base64.StdEncoding.EncodeToString(payload)
	response := map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "resource", "resource": map[string]any{
			"uri": "file:///bin", "mimeType": "application/octet-stream", "blob": blob,
		}},
		map[string]any{"type": "image", "mimeType": "image/png", "data": blob},
	}}
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) { return response, nil },
	}}
	c := newFakeClient("fake", ft, time.Second)

	got, err := c.CallTool(context.Background(), "render", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	// The resource blob contributes nothing and the image keeps its placeholder.
	if want := "hello\n[image: image/png]"; got != want {
		t.Fatalf("output changed with feature off: got %q, want %q", got, want)
	}
	if strings.Contains(got, "Binary resource saved") {
		t.Fatalf("artifact marker leaked with feature off: %q", got)
	}

	// A blob-only resource used to fall through to the raw JSON payload; keep that.
	blobOnly := map[string]any{"content": []any{
		map[string]any{"type": "resource", "resource": map[string]any{"blob": blob, "mimeType": "image/png"}},
	}}
	ft2 := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) { return blobOnly, nil },
	}}
	c2 := newFakeClient("fake", ft2, time.Second)
	got2, err := c2.CallTool(context.Background(), "render", nil)
	if err != nil {
		t.Fatalf("CallTool (blob only): %v", err)
	}
	wantRaw, _ := json.Marshal(blobOnly)
	if got2 != string(wantRaw) {
		t.Fatalf("blob-only fallback changed: got %q, want raw %q", got2, wantRaw)
	}
}

func TestCallToolBlobWithArtifactDirSaves(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(blobArtifactDirEnv, dir)

	payload := []byte("%PDF-1.4 fake pdf bytes \x00\xff")
	blob := base64.StdEncoding.EncodeToString(payload)
	response := map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "seen"},
		map[string]any{"type": "resource", "resource": map[string]any{
			"uri": "file:///doc.pdf", "mimeType": "application/pdf", "blob": blob,
		}},
	}}
	ft := &fakeTransport{handlers: map[string]func(json.RawMessage) (any, error){
		"tools/call": func(json.RawMessage) (any, error) { return response, nil },
	}}
	c := newFakeClient("fake", ft, time.Second)

	got, err := c.CallTool(context.Background(), "render", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	const marker = "[Binary resource saved to "
	start := strings.Index(got, marker)
	if start < 0 {
		t.Fatalf("missing artifact marker: %q", got)
	}
	if !strings.Contains(got, "seen") {
		t.Fatalf("text block lost: %q", got)
	}
	rest := got[start+len(marker):]
	end := strings.Index(rest, "]")
	if end < 0 {
		t.Fatalf("unterminated artifact marker: %q", got)
	}
	path := rest[:end]
	if !pathWithinDir(dir, path) {
		t.Fatalf("artifact path escaped dir: %q", path)
	}
	if !strings.HasSuffix(path, ".pdf") {
		t.Fatalf("unexpected artifact extension: %q", path)
	}
	if data, err := os.ReadFile(path); err != nil {
		t.Fatalf("read artifact: %v", err)
	} else if !bytes.Equal(data, payload) {
		t.Fatalf("artifact content = %q, want %q", data, payload)
	}
}
