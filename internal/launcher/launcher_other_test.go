//go:build !windows

package launcher

import "testing"

func TestMacCandidatePath(t *testing.T) {
	got := macCandidates()
	want := []candidate{{`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`, "chrome"}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("macCandidates() = %#v, want %#v", got, want)
	}
}

func TestLinuxCandidates(t *testing.T) {
	got := linuxCandidates()
	want := []candidate{
		{"chromium", "chrome"},
		{"google-chrome", "chrome"},
		{"chromium-browser", "chrome"},
	}
	if len(got) != len(want) {
		t.Fatalf("linuxCandidates() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("linuxCandidates()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}
