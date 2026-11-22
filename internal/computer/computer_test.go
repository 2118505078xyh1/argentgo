package computer

import (
	"bytes"
	"image/png"
	"runtime"
	"testing"
)

func TestCapture(t *testing.T) {
	img, err := Capture()
	if runtime.GOOS != "windows" {
		if err == nil {
			t.Fatal("expected unsupported error on non-Windows platform")
		}
		if img != nil {
			t.Fatal("expected nil image on non-Windows platform")
		}
		return
	}
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if img == nil {
		t.Fatal("nil image")
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Fatalf("invalid dimensions: %v", img.Bounds())
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	if buf.Len() < 1000 {
		t.Fatalf("suspiciously small screenshot: %d bytes", buf.Len())
	}
	t.Logf("captured %dx%d, png %d bytes", img.Bounds().Dx(), img.Bounds().Dy(), buf.Len())
}

func TestKeyRejectsUnknown(t *testing.T) {
	if err := Key("definitely-not-a-key"); err == nil {
		t.Fatal("expected error for unknown key")
	}
}
