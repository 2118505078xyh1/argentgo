package memory

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"unicode/utf8"
)

// 本文件是 PlanReclaim / ReclaimSummary 的差分与属性守护：
// 用一份刻意写法不同的简单参考实现逐项对照随机候选，覆盖年龄边界、
// 被取代/无用标记、CJK 键与空键；并守护摘要的顺序无关、无控制字符、
// 不改入参与重复调用确定性。全部测试只依赖固定种子，结果可复现。

const (
	guardSeed        int64 = 20261121
	guardIterations        = 100
	guardDeterminism       = 100
)

// 参考键池：ASCII、含空格、CJK、带路径与扩展名，以及空键。
// 刻意不含 C0/DEL/C1 控制字符：摘要按原样渲染键，控制字符会破坏单行可读性。
var guardKeys = []string{
	"", "a", "Z", "alpha", "beta", "key-1", "a b", " ", "  ",
	"密钥", "项目/设计", "凭证.txt", "回收/归档", "空白 ",
}

// referencePlanReclaim 是 PlanReclaim 的独立参考实现。
// 判定顺序与生产代码不同（先合并标记再比年龄），排序用 sort.Strings
// 而非 sort.SliceStable：重复键的字符串完全相同，稳定与否不影响最终切片值。
// 因此它既能抓判定错误，也能抓「排序漏了/排错方向」的回归。
func referencePlanReclaim(cands []ReclaimCandidate, maxAge int) (evict, keep []string) {
	for _, c := range cands {
		marked := c.Superseded || c.Useless
		if marked || c.Age > maxAge {
			evict = append(evict, c.Key)
		} else {
			keep = append(keep, c.Key)
		}
	}
	sort.Strings(evict)
	sort.Strings(keep)
	return evict, keep
}

// guardMaxAges 覆盖负、零、正的阈值，便于把年龄边界推到各种符号区间。
var guardMaxAges = []int{-3, -1, 0, 1, 5, 10}

// cloneCands / cloneStrings 保留 nil 与空切片的区别：
// reflect.DeepEqual 会把二者判为不等，直接 append 复制会把空切片变成 nil。
func cloneCands(in []ReclaimCandidate) []ReclaimCandidate {
	if in == nil {
		return nil
	}
	out := make([]ReclaimCandidate, len(in))
	copy(out, in)
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// randomReclaimCandidates 生成一批随机候选。
// 年龄多数落在 maxAge-1 / maxAge / maxAge+1 三个边界上，
// 保证每轮差分都压到「等于阈值保留、超一即淘汰」的判据。
func randomReclaimCandidates(rng *rand.Rand, maxAge int) []ReclaimCandidate {
	boundary := [...]int{maxAge - 1, maxAge, maxAge + 1}
	n := rng.Intn(12) // 含 0：顺带覆盖空输入
	out := make([]ReclaimCandidate, 0, n)
	for i := 0; i < n; i++ {
		age := boundary[rng.Intn(len(boundary))]
		if rng.Intn(3) == 0 {
			age = rng.Intn(41) - 20 // 掺入远离边界的随机年龄
		}
		out = append(out, ReclaimCandidate{
			Key:        guardKeys[rng.Intn(len(guardKeys))],
			Age:        age,
			Superseded: rng.Intn(2) == 0,
			Useless:    rng.Intn(2) == 0,
			Bytes:      rng.Intn(1 << 20), // 元数据：不应影响判定
		})
	}
	return out
}

// TestPlanReclaimDifferential 对 100 组随机候选逐项比对 evict/keep，
// 并同时守护排序、划分完备性、键的 UTF-8 完整性以及入参不被修改。
func TestPlanReclaimDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(guardSeed))
	for i := 0; i < guardIterations; i++ {
		maxAge := guardMaxAges[rng.Intn(len(guardMaxAges))]
		cands := randomReclaimCandidates(rng, maxAge)
		before := cloneCands(cands)

		gotEvict, gotKeep := PlanReclaim(cands, maxAge)
		wantEvict, wantKeep := referencePlanReclaim(cands, maxAge)

		if !reflect.DeepEqual(gotEvict, wantEvict) {
			t.Fatalf("iter %d maxAge=%d evict = %q, ref = %q (cands=%#v)", i, maxAge, gotEvict, wantEvict, cands)
		}
		if !reflect.DeepEqual(gotKeep, wantKeep) {
			t.Fatalf("iter %d maxAge=%d keep = %q, ref = %q (cands=%#v)", i, maxAge, gotKeep, wantKeep, cands)
		}
		if !reflect.DeepEqual(cands, before) {
			t.Fatalf("iter %d input mutated: got %#v, want %#v", i, cands, before)
		}
		if len(gotEvict)+len(gotKeep) != len(cands) {
			t.Fatalf("iter %d partition lost items: evict=%d keep=%d input=%d",
				i, len(gotEvict), len(gotKeep), len(cands))
		}
		if !sort.StringsAreSorted(gotEvict) || !sort.StringsAreSorted(gotKeep) {
			t.Fatalf("iter %d output not sorted: evict=%q keep=%q", i, gotEvict, gotKeep)
		}
		for _, key := range append(append([]string(nil), gotEvict...), gotKeep...) {
			if !utf8.ValidString(key) {
				t.Fatalf("iter %d broken UTF-8 key %q", i, key)
			}
		}
	}
}

// TestPlanReclaimBoundaryGrid 用小巧的穷举网格钉死年龄边界：
// 对每个阈值，把 maxAge-1 / maxAge / maxAge+1 与四种标记组合交叉比对参考实现。
func TestPlanReclaimBoundaryGrid(t *testing.T) {
	flags := []struct{ superseded, useless bool }{
		{false, false}, {true, false}, {false, true}, {true, true},
	}
	for _, maxAge := range guardMaxAges {
		for _, age := range []int{maxAge - 1, maxAge, maxAge + 1} {
			for _, f := range flags {
				cands := []ReclaimCandidate{
					{Key: "边界", Age: age, Superseded: f.superseded, Useless: f.useless},
					{Key: "", Age: age + 1, Superseded: f.useless, Useless: f.superseded},
				}
				gotEvict, gotKeep := PlanReclaim(cands, maxAge)
				wantEvict, wantKeep := referencePlanReclaim(cands, maxAge)
				if !reflect.DeepEqual(gotEvict, wantEvict) || !reflect.DeepEqual(gotKeep, wantKeep) {
					t.Fatalf("maxAge=%d age=%d flags=(%v,%v): evict=%q keep=%q, ref=(%q,%q)",
						maxAge, age, f.superseded, f.useless, gotEvict, gotKeep, wantEvict, wantKeep)
				}
			}
		}
	}
}

// randomKeyList 生成一份长度不定的键名单，供摘要属性测试使用。
func randomKeyList(rng *rand.Rand, keys []string) []string {
	n := rng.Intn(10)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, keys[rng.Intn(len(keys))])
	}
	return out
}

// assertNoControlChars 断言字符串里没有 C0、DEL 或 C1 控制字符。
func assertNoControlChars(t *testing.T, s string) {
	t.Helper()
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("summary contains control character %U in %q", r, s)
		}
	}
}

// TestReclaimSummaryGuard 在 100 组随机名单上守护摘要的四项属性：
// 顺序无关、输出无控制字符、不改入参、重复调用确定。
func TestReclaimSummaryGuard(t *testing.T) {
	rng := rand.New(rand.NewSource(guardSeed + 1))
	for i := 0; i < guardIterations; i++ {
		evict := randomKeyList(rng, guardKeys)
		keep := randomKeyList(rng, guardKeys)
		evictBefore := cloneStrings(evict)
		keepBefore := cloneStrings(keep)

		first := ReclaimSummary(evict, keep)

		if !reflect.DeepEqual(evict, evictBefore) || !reflect.DeepEqual(keep, keepBefore) {
			t.Fatalf("iter %d input mutated: evict=%q keep=%q", i, evict, keep)
		}
		assertNoControlChars(t, first)

		// 分别打乱两个名单后重算：摘要只取决于内容，不取决于传入顺序。
		shuffledEvict := cloneStrings(evict)
		shuffledKeep := cloneStrings(keep)
		rng.Shuffle(len(shuffledEvict), func(a, b int) {
			shuffledEvict[a], shuffledEvict[b] = shuffledEvict[b], shuffledEvict[a]
		})
		rng.Shuffle(len(shuffledKeep), func(a, b int) {
			shuffledKeep[a], shuffledKeep[b] = shuffledKeep[b], shuffledKeep[a]
		})
		if got := ReclaimSummary(shuffledEvict, shuffledKeep); got != first {
			t.Fatalf("iter %d order-dependent: %q vs %q", i, got, first)
		}

		// 100 次确定性：同一输入重复渲染必须逐字节一致。
		for j := 0; j < guardDeterminism; j++ {
			if got := ReclaimSummary(evict, keep); got != first {
				t.Fatalf("iter %d run %d differs: %q vs %q", i, j, got, first)
			}
		}
	}
}
