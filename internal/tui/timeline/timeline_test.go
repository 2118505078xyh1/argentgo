package timeline

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.timeline")
	r, err := NewRecorder(path, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	// 注入固定时钟，使 at_ms 可预测。
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	r.setClock(func() time.Time { return now })
	r.Write([]byte("frame1"))
	now = now.Add(100 * time.Millisecond)
	r.Write([]byte("frame2"))
	now = now.Add(50 * time.Millisecond)
	r.Resize(100, 30)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h := p.Header()
	if h.Cols != 80 || h.Rows != 24 || h.Version != formatVersion {
		t.Fatalf("header = %+v", h)
	}
	var out bytes.Buffer
	resizes := 0
	if err := p.Replay(&out, false, func(c, r int) {
		resizes++
		if c != 100 || r != 30 {
			t.Errorf("resize = %d,%d", c, r)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "frame1frame2" {
		t.Fatalf("replayed = %q", out.String())
	}
	if resizes != 1 {
		t.Fatalf("resize callbacks = %d", resizes)
	}
}

func TestSnapshotFinalFrame(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.timeline")
	r, _ := NewRecorder(path, 80, 24)
	r.setClock(func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) })
	r.Write([]byte("a"))
	r.Write([]byte("b"))
	r.Close()

	p, _ := Load(path)
	snap, err := p.Snapshot()
	if err != nil || string(snap) != "ab" {
		t.Fatalf("snapshot = %q %v", snap, err)
	}
}

func TestLoadRejectsMissingHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.timeline")
	os.WriteFile(path, []byte(`{"kind":"output","at_ms":0,"data":"YQ=="}`+"\n"), 0o600)
	if _, err := Load(path); !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("err = %v, want ErrInvalidHeader", err)
	}
}

func TestLoadRejectsWrongVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.timeline")
	os.WriteFile(path, []byte(`{"kind":"header","version":999,"cols":80,"rows":24}`+"\n"), 0o600)
	if _, err := Load(path); !errors.Is(err, ErrWrongVersion) {
		t.Fatalf("err = %v, want ErrWrongVersion", err)
	}
}

func TestLoadRejectsCorruptEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.timeline")
	os.WriteFile(path, []byte(`{"kind":"header","version":1,"cols":80,"rows":24}`+"\nnot-json\n"), 0o600)
	if _, err := Load(path); !errors.Is(err, ErrCorruptEvent) {
		t.Fatalf("err = %v, want ErrCorruptEvent", err)
	}
}

func TestRecorderWriteAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.timeline")
	r, _ := NewRecorder(path, 80, 24)
	r.Close()
	if _, err := r.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("err = %v, want os.ErrClosed", err)
	}
}

func TestReplayBadBase64(t *testing.T) {
	p := &Player{events: []Event{{Kind: KindOutput, Data: "!!!not-base64"}}}
	if err := p.Replay(&bytes.Buffer{}, false, nil); !errors.Is(err, ErrCorruptEvent) {
		t.Fatalf("err = %v", err)
	}
}

func TestRecorderMonotonicTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.timeline")
	r, _ := NewRecorder(path, 80, 24)
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	r.setClock(func() time.Time { return now })
	r.Write([]byte("a"))
	now = base.Add(-10 * time.Millisecond) // 时钟回拨
	r.Write([]byte("b"))
	r.Close()

	p, _ := Load(path)
	events := p.Events()
	if len(events) != 2 || events[1].AtMS < events[0].AtMS {
		t.Fatalf("timestamps not monotonic: %+v", events)
	}
}
