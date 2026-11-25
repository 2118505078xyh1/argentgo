package provider_test

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"example.com/argentgo/internal/provider"
)

// probeFrom 构建确定性的注入式探针：failed 中的每个 URL 报告不健康，其他都健康。它不执行网络 I/O 且可安全并发读取，因此同一探针可被下面的纯函数测试复用。
// 报告不健康，其他都健康。它不执行网络 I/O
// 且可安全并发读取，因此同一探针可被下面的
// 纯函数测试复用。
func probeFrom(failed ...string) func(string) error {
	bad := make(map[string]struct{}, len(failed))
	for _, url := range failed {
		bad[url] = struct{}{}
	}
	return func(url string) error {
		if _, ok := bad[url]; ok {
			return errors.New("unhealthy: " + url)
		}
		return nil
	}
}

func TestProbeHealthyFiltersFailingEndpointsAndPreservesOrder(t *testing.T) {
	urls := []string{
		"http://a.test/v1",
		"http://b.test/v1",
		"http://c.test/v1",
		"http://d.test/v1",
	}
	got := provider.ProbeHealthy(urls, probeFrom("http://b.test/v1", "http://d.test/v1"))
	want := []string{"http://a.test/v1", "http://c.test/v1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProbeHealthy = %#v, want %#v", got, want)
	}
}

func TestProbeHealthyDeduplicatesAndProbesEachURLOnce(t *testing.T) {
	urls := []string{
		"http://a.test/v1",
		"http://b.test/v1",
		"http://a.test/v1", // duplicate of a healthy endpoint
		"http://c.test/v1",
		"http://b.test/v1", // duplicate of a failing endpoint
		"http://a.test/v1",
	}

	var mu sync.Mutex
	calls := make(map[string]int)
	probe := func(url string) error {
		mu.Lock()
		calls[url]++
		mu.Unlock()
		if url == "http://b.test/v1" {
			return errors.New("unhealthy")
		}
		return nil
	}

	got := provider.ProbeHealthy(urls, probe)
	want := []string{"http://a.test/v1", "http://c.test/v1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProbeHealthy = %#v, want %#v", got, want)
	}

	wantCalls := map[string]int{
		"http://a.test/v1": 1,
		"http://b.test/v1": 1,
		"http://c.test/v1": 1,
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("probe calls = %#v, want one per unique URL %#v", calls, wantCalls)
	}
}

func TestProbeHealthyNilProbeKeepsEveryUniqueURL(t *testing.T) {
	urls := []string{"http://a.test/v1", "http://b.test/v1", "http://a.test/v1"}
	got := provider.ProbeHealthy(urls, nil)
	want := []string{"http://a.test/v1", "http://b.test/v1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProbeHealthy with nil probe = %#v, want %#v", got, want)
	}
}

func TestProbeHealthyEmptyInputReturnsNonNilEmptySlice(t *testing.T) {
	for name, input := range map[string][]string{
		"nil":   nil,
		"empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			got := provider.ProbeHealthy(input, probeFrom())
			if got == nil {
				t.Fatal("ProbeHealthy returned a nil slice, want a non-nil empty slice")
			}
			if len(got) != 0 {
				t.Fatalf("ProbeHealthy = %#v, want empty", got)
			}
		})
	}
}

func TestProbeHealthyAllUnhealthyReturnsEmpty(t *testing.T) {
	urls := []string{"http://a.test/v1", "http://b.test/v1"}
	got := provider.ProbeHealthy(urls, probeFrom(urls...))
	if len(got) != 0 {
		t.Fatalf("ProbeHealthy = %#v, want empty when every endpoint fails", got)
	}
}

func TestProbeHealthyResultDoesNotAliasInput(t *testing.T) {
	urls := []string{"http://a.test/v1", "http://b.test/v1"}
	got := provider.ProbeHealthy(urls, nil)
	got[0] = "http://mutated.test/v1"
	if urls[0] != "http://a.test/v1" {
		t.Fatal("mutating the result changed the input slice")
	}
}

func TestProbeHealthyIsPureUnderConcurrentUse(t *testing.T) {
	urls := []string{
		"http://a.test/v1",
		"http://b.test/v1",
		"http://c.test/v1",
		"http://a.test/v1",
	}
	probe := probeFrom("http://b.test/v1")
	want := []string{"http://a.test/v1", "http://c.test/v1"}

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if got := provider.ProbeHealthy(urls, probe); !reflect.DeepEqual(got, want) {
					errs <- "ProbeHealthy gave an inconsistent result"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// 调用方的输入绝不能被探针运行修改。
	if urls[0] != "http://a.test/v1" || len(urls) != 4 {
		t.Fatal("ProbeHealthy mutated its input")
	}
}
