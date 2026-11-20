package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsCancelError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"direct", context.Canceled, true},
		{"wrapped", fmt.Errorf("prompt failed: %w", context.Canceled), true},
		{"legacy rpc message", errors.New("context canceled"), true},
		{"stop reason message", errors.New("prompt cancelled"), true},
		{"real failure", errors.New("provider returned 500"), false},
		{"deadline is a failure", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isCancelError(tc.err); got != tc.want {
				t.Fatalf("isCancelError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestFinishTextKeepsStreamedOnCancellation guards the native symptom: an
// aborted reply must keep the text the user already saw stream in, while a real
// failure keeps the engine's own result.
func TestFinishTextKeepsStreamedOnCancellation(t *testing.T) {
	cases := []struct {
		name     string
		full     string
		err      error
		streamed string
		want     string
	}{
		{"success full answer", "answer", nil, "answer", "answer"},
		{"success without stream", "answer", nil, "", "answer"},
		{"normalised cancelled result", "partial", nil, "partial", "partial"},
		{"empty result keeps stream", "", nil, "partial", "partial"},
		{"legacy engine error keeps stream", "", errors.New("context canceled"), "partial", "partial"},
		{"real failure keeps engine result", "", errors.New("boom"), "partial", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := finishText(tc.full, tc.err, tc.streamed); got != tc.want {
				t.Fatalf("finishText(%q, %v, %q) = %q, want %q",
					tc.full, tc.err, tc.streamed, got, tc.want)
			}
		})
	}
}
