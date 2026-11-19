package llm_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/llm"
)

func TestResolveAuthUsesFirstNonEmptyEnv(t *testing.T) {
	env := map[string]string{
		"PRIMARY":   "   ",
		"SECONDARY": "second-key",
		"THIRD":     "third-key",
	}
	lookup := func(name string) string { return env[name] }

	cases := []struct {
		name       string
		names      []string
		header     string
		scheme     string
		wantHeader string
		wantValue  string
	}{
		{
			name:       "first empty then second wins",
			names:      []string{"PRIMARY", "SECONDARY", "THIRD"},
			header:     "x-api-key",
			wantHeader: "x-api-key",
			wantValue:  "second-key",
		},
		{
			name:       "bearer scheme",
			names:      []string{"PRIMARY", "SECONDARY"},
			header:     "Authorization",
			scheme:     "Bearer",
			wantHeader: "Authorization",
			wantValue:  "Bearer second-key",
		},
		{
			name:   "all empty sends no header",
			names:  []string{"PRIMARY", "EMPTY"},
			header: "Authorization",
			scheme: "Bearer",
		},
		{
			name:   "no names sends no header",
			names:  nil,
			header: "x-api-key",
		},
		{
			name:   "no auth sentinel sends no header",
			names:  []string{"SECONDARY"},
			header: "x-api-key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := llm.ResolveAuth(lookup, tc.names, tc.header, tc.scheme)
			if tc.name == "no auth sentinel sends no header" {
				auth = llm.NoAuth()
			}
			if auth.Header != tc.wantHeader || auth.Value.Value() != tc.wantValue {
				t.Fatalf("auth = %q/%q, want %q/%q", auth.Header, auth.Value.Value(), tc.wantHeader, tc.wantValue)
			}
			headers := make(http.Header)
			headers.Set("Accept", "text/event-stream")
			auth.Apply(headers)
			if got := headers.Get(tc.header); got != tc.wantValue {
				t.Fatalf("%s = %q, want %q", tc.header, got, tc.wantValue)
			}
			if got := headers.Get("Accept"); got != "text/event-stream" {
				t.Fatalf("Apply removed an unrelated header: %q", got)
			}
			if tc.wantValue == "" && len(headers) != 1 {
				t.Fatalf("no-auth Apply added headers: %#v", headers)
			}
			if strings.Contains(fmt.Sprintf("%+v", auth), tc.wantValue) && tc.wantValue != "" {
				t.Fatalf("auth formatting leaked the credential: %v", auth)
			}
		})
	}
}

func TestAuthOrElseKeepsTheFirstCredential(t *testing.T) {
	first := llm.Auth{Header: "Authorization", Value: llm.NewSecret("Bearer first")}
	fallback := llm.Auth{Header: "x-api-key", Value: llm.NewSecret("second")}

	if got := first.OrElse(fallback); got.Header != first.Header || got.Value.Value() != "Bearer first" {
		t.Fatalf("OrElse replaced a present credential: %+v", got)
	}
	if got := llm.NoAuth().OrElse(fallback); got.Header != fallback.Header || got.Value.Value() != "second" {
		t.Fatalf("OrElse did not fall back: %+v", got)
	}
}
