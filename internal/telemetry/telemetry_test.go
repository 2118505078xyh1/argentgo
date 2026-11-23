package telemetry

import (
	"context"
	"testing"
	"time"
)

func TestNoopProvider(t *testing.T) {
	SetProvider(nil)
	p := Current()
	ctx, span := StartSpan(context.Background(), "x")
	if ctx == nil {
		t.Fatal("nil ctx")
	}
	span.SetAttribute("k", "v")
	span.End()
	p.Counter("c").Add(1)
	p.Histogram("h").Record(1.5)
	if p.Tracer() == nil {
		t.Fatal("nil tracer")
	}
	if d := ElapsedMS(time.Now(), time.Now().Add(time.Second)); d < 999 || d > 1001 {
		t.Fatalf("ElapsedMS = %v", d)
	}
}
