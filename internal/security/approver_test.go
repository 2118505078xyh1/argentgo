package security

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPromptApproverReadsAnswers(t *testing.T) {
	cases := []struct {
		answer string
		want   bool
	}{
		{"y\n", true},
		{"yes\n", true},
		{"Y\r\n", true},
		{"  yes  \n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"maybe\n", false},
	}
	for _, tc := range cases {
		var out bytes.Buffer
		approver := NewPromptApprover(strings.NewReader(tc.answer), &out)
		got, err := approver.Ask(context.Background(), "bash", `{"command":"ls"}`)
		if err != nil {
			t.Fatalf("answer %q: %v", tc.answer, err)
		}
		if got != tc.want {
			t.Fatalf("answer %q: got %v, want %v", tc.answer, got, tc.want)
		}
		if !strings.Contains(out.String(), "Allow bash") {
			t.Fatalf("answer %q: prompt %q does not name the tool", tc.answer, out.String())
		}
	}
}

func TestPromptApproverKeepsTheReaderPositioned(t *testing.T) {
	var out bytes.Buffer
	approver := NewPromptApprover(strings.NewReader("y\nn\n"), &out)
	if ok, err := approver.Ask(context.Background(), "bash", "{}"); err != nil || !ok {
		t.Fatalf("first answer: ok=%v err=%v", ok, err)
	}
	if ok, err := approver.Ask(context.Background(), "write", "{}"); err != nil || ok {
		t.Fatalf("second answer: ok=%v err=%v", ok, err)
	}
}

func TestPromptApproverDeniesOnEOFAndCancellation(t *testing.T) {
	approver := NewPromptApprover(strings.NewReader(""), &bytes.Buffer{})
	if ok, err := approver.Ask(context.Background(), "bash", "{}"); err != nil || ok {
		t.Fatalf("EOF must deny: ok=%v err=%v", ok, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := approver.Ask(ctx, "bash", "{}"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: got %v", err)
	}
}

func TestNeverApproverAlwaysDenies(t *testing.T) {
	ok, err := NeverApprover{}.Ask(context.Background(), "read", `{"path":"a.txt"}`)
	if err != nil || ok {
		t.Fatalf("got ok=%v err=%v, want a plain deny", ok, err)
	}
}

type countingApprover struct {
	calls int
	allow bool
}

func (c *countingApprover) Ask(ctx context.Context, name string, args string) (bool, error) {
	c.calls++
	return c.allow, nil
}

func TestCachedApproverReusesAnAllowPerTool(t *testing.T) {
	inner := &countingApprover{allow: true}
	cached := NewCachedApprover(inner)
	for i := 0; i < 3; i++ {
		if ok, err := cached.Ask(context.Background(), "bash", "{}"); err != nil || !ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("bash prompted %d times, want once", inner.calls)
	}
	if ok, err := cached.Ask(context.Background(), "write", "{}"); err != nil || !ok {
		t.Fatalf("write: ok=%v err=%v", ok, err)
	}
	if inner.calls != 2 {
		t.Fatalf("a different tool must prompt again: %d calls", inner.calls)
	}
	if got := len(cached.Allowed()); got != 2 {
		t.Fatalf("cached %d tool names, want 2", got)
	}
}

func TestCachedApproverDoesNotCacheARefusal(t *testing.T) {
	inner := &countingApprover{allow: false}
	cached := NewCachedApprover(inner)
	for i := 0; i < 2; i++ {
		if ok, err := cached.Ask(context.Background(), "bash", "{}"); err != nil || ok {
			t.Fatalf("call %d: ok=%v err=%v", i, ok, err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("a refusal must not be cached: %d calls", inner.calls)
	}
	if got := cached.Allowed(); len(got) != 0 {
		t.Fatalf("refused tools must not be remembered: %v", got)
	}
}

func TestCachedApproverWithoutAnInnerDenies(t *testing.T) {
	if ok, err := NewCachedApprover(nil).Ask(context.Background(), "bash", "{}"); err != nil || ok {
		t.Fatalf("got ok=%v err=%v, want deny", ok, err)
	}
}
