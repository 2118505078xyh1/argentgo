package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2118505078xyh1/argentgo/internal/tui/timeline"
)

func makeTimelineFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.timeline")
	r, err := timeline.NewRecorder(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	r.Write([]byte("frame-"))
	r.Write([]byte("final"))
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func testTimelineApp(t *testing.T) (App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	return App{IO: IO{Out: &out, Err: &errOut}}, &out, &errOut
}

func TestTimelineCommandInfo(t *testing.T) {
	app, out, errOut := testTimelineApp(t)
	path := makeTimelineFixture(t)
	if code := runTimeline(app, context.Background(), []string{"info", path}); code != 0 {
		t.Fatalf("info code=%d err=%q", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "cols=80") || !strings.Contains(got, "rows=24") || !strings.Contains(got, "events=2") {
		t.Fatalf("info output = %q", got)
	}
}

func TestTimelineCommandReplay(t *testing.T) {
	app, out, errOut := testTimelineApp(t)
	path := makeTimelineFixture(t)
	if code := runTimeline(app, context.Background(), []string{"replay", path}); code != 0 {
		t.Fatalf("replay code=%d err=%q", code, errOut.String())
	}
	if out.String() != "frame-final" {
		t.Fatalf("replay = %q", out.String())
	}
}

func TestTimelineCommandHash(t *testing.T) {
	app, out, _ := testTimelineApp(t)
	path := makeTimelineFixture(t)
	if code := runTimeline(app, context.Background(), []string{"hash", path}); code != 0 {
		t.Fatalf("hash code=%d", code)
	}
	// SHA-256 of "frame-final" (64 hex chars)
	if len(strings.TrimSpace(out.String())) != 64 {
		t.Fatalf("hash output = %q", out.String())
	}
}

func TestTimelineCommandErrors(t *testing.T) {
	app, _, errOut := testTimelineApp(t)
	if code := runTimeline(app, context.Background(), []string{"info", "/nonexistent"}); code == 0 {
		t.Fatal("missing file should fail")
	}
	if errOut.String() == "" {
		t.Fatal("error should be printed")
	}
	if code := runTimeline(app, context.Background(), []string{"bogus", "/nonexistent"}); code == 0 {
		t.Fatal("unknown subcommand should fail")
	}
}
