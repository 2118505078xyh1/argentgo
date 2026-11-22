package memory

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestStemWord 覆盖每种后缀规则以及保守性守卫。
func TestStemWord(t *testing.T) {
	tests := []struct {
		word string
		want string
	}{
		// 单个 s
		{"backups", "backup"},
		{"runs", "run"},
		{"days", "day"},
		{"backup", "backup"}, // 无后缀：原样
		// es
		{"boxes", "box"},
		{"watches", "watch"},
		{"passes", "pass"}, // pass -> passes 是合法复数
		// ies / ied
		{"policies", "policy"},
		{"companies", "company"},
		{"tried", "try"},
		{"denied", "deny"},
		// ed
		{"walked", "walk"},
		{"created", "creat"},
		// ing（含双写辅音归一）
		{"walking", "walk"},
		{"running", "run"},
		{"testing", "test"},
		{"building", "build"},
		// 保守守卫：短词、ss/us 结尾、含数字、CJK 不还原
		{"pass", "pass"},
		{"class", "class"},
		{"plus", "plus"},
		{"gas", "gas"},
		{"bus", "bus"},
		{"is", "is"},
		{"as", "as"},
		{"yes", "yes"},
		{"ties", "tie"},
		{"going", "going"},
		{"1password", "1password"},
		{"1passwords", "1passwords"},
		{"备份", "备份"},
		{"数据库", "数据库"},
	}
	for _, tc := range tests {
		t.Run(tc.word, func(t *testing.T) {
			if got := stemWord(tc.word); got != tc.want {
				t.Fatalf("stemWord(%q) = %q, want %q", tc.word, got, tc.want)
			}
		})
	}
}

// TestRecallWithStemmingBackupsMatchesBackup 守护 R52 #50 的核心诉求：
// "backups" 能召回 "backup"（默认 Recall 做不到，需 opt-in）。
func TestRecallWithStemmingBackupsMatchesBackup(t *testing.T) {
	entries := []Memory{
		{ID: "backup", Text: "restore from the nightly backup", TS: time.Unix(10, 0)},
		{ID: "other", Text: "water the plants", TS: time.Unix(20, 0)},
	}
	for _, query := range []string{"backups", "backup", "BACKUPS"} {
		got := RecallWithStemming(query, entries)
		if len(got) != 1 || got[0].ID != "backup" {
			t.Fatalf("RecallWithStemming(%q) = %#v, want only [backup]", query, got)
		}
	}
	if got := RecallWithStemming("updated backups", entries); len(got) != 1 || got[0].ID != "backup" {
		t.Fatalf("RecallWithStemming with extra query words = %#v, want [backup]", got)
	}
}

// TestRecallWithStemmingOnePasswordNotPass 确保词形还原不会把 "1Password"
// 折叠成 "pass"：token 级还原 + 纯字母限制 + ss 守卫共同保证这一点。
func TestRecallWithStemmingOnePasswordNotPass(t *testing.T) {
	entries := []Memory{
		{ID: "pw", Text: "uses 1Password for the vault", TS: time.Unix(1, 0)},
		{ID: "pass", Text: "a pass for the test", TS: time.Unix(2, 0)},
	}

	got := RecallWithStemming("pass", entries)
	if len(got) != 1 || got[0].ID != "pass" {
		t.Fatalf("query pass matched %#v, want only [pass] (1Password must not match)", got)
	}

	got = RecallWithStemming("1password", entries)
	if len(got) != 1 || got[0].ID != "pw" {
		t.Fatalf("query 1password matched %#v, want only [pw]", got)
	}

	// password/passwords 仍是可还原的合法复数对。
	pwEntries := []Memory{{ID: "pw2", Text: "rotate the password", TS: time.Unix(3, 0)}}
	if got = RecallWithStemming("passwords", pwEntries); len(got) != 1 || got[0].ID != "pw2" {
		t.Fatalf("passwords -> password failed: %#v", got)
	}
}

// TestRecallWithStemmingChinese 验证中文按单字标记召回，且词形还原完全不
// 影响 CJK 文本。
func TestRecallWithStemmingChinese(t *testing.T) {
	entries := []Memory{
		{ID: "zh", Text: "每天备份数据库", TS: time.Unix(1, 0)},
		{ID: "en", Text: "daily backup of the database", TS: time.Unix(2, 0)},
	}
	got := RecallWithStemming("备份", entries)
	if len(got) != 1 || got[0].ID != "zh" {
		t.Fatalf("Chinese recall = %#v, want only [zh]", got)
	}
	got = RecallWithStemming("数据库", entries)
	if len(got) != 1 || got[0].ID != "zh" {
		t.Fatalf("Chinese 3-char recall = %#v, want only [zh]", got)
	}
}

// TestRecallWithStemmingSuffixRules 表驱动验证每条词形规则都能跨形态召回，
// 并验证不应跨形态误匹配的保守情形。
func TestRecallWithStemmingSuffixRules(t *testing.T) {
	matches := []struct {
		query string
		text  string
	}{
		{"backups", "one backup"},         // s
		{"boxes", "a box"},                // es
		{"watches", "watch it"},           // es
		{"policies", "the policy"},        // ies
		{"denied", "deny the request"},    // ied -> y
		{"walked", "walk the dog"},        // ed
		{"running", "run the job"},        // ing
		{"walked", "walks daily"},         // 跨后缀同词根
		{"loaded", "load the file"},       // ed 规则：loaded -> load
		{"backups", "backups everywhere"}, // 完全相同的复数
	}
	for _, tc := range matches {
		t.Run(tc.query+"~"+tc.text, func(t *testing.T) {
			entries := []Memory{{ID: "doc", Text: tc.text, TS: time.Unix(1, 0)}}
			got := RecallWithStemming(tc.query, entries)
			if len(got) != 1 || got[0].ID != "doc" {
				t.Fatalf("RecallWithStemming(%q) on %q = %#v, want [doc]", tc.query, tc.text, got)
			}
		})
	}

	noMatches := []struct {
		query string
		text  string
	}{
		{"pass", "uses 1Password"},
		{"class", "a pass"},
		{"bus", "gas station"},
		{"is", "as expected"},
	}
	for _, tc := range noMatches {
		t.Run("no/"+tc.query+"~"+tc.text, func(t *testing.T) {
			entries := []Memory{{ID: "doc", Text: tc.text, TS: time.Unix(1, 0)}}
			if got := RecallWithStemming(tc.query, entries); len(got) != 0 {
				t.Fatalf("RecallWithStemming(%q) on %q = %#v, want no hits", tc.query, tc.text, got)
			}
		})
	}
}

// TestRecallWithStemmingIsPureAndDeterministic 守护纯函数契约：不改入参、
// 不与入参共享外层底层数组、重复调用结果一致。
func TestRecallWithStemmingIsPureAndDeterministic(t *testing.T) {
	entries := []Memory{
		{ID: "a", Text: "backups of policies", Tags: []string{"ops"}, TS: time.Unix(1, 0)},
		{ID: "b", Text: "backup was denied", Tags: []string{"sec"}, TS: time.Unix(2, 0)},
	}
	snapshot := append([]Memory(nil), entries...)

	first := RecallWithStemming("backups denied", entries)
	second := RecallWithStemming("backups denied", entries)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("not deterministic: %#v vs %#v", first, second)
	}
	if !reflect.DeepEqual(entries, snapshot) {
		t.Fatalf("input mutated: %#v, want %#v", entries, snapshot)
	}
	if len(first) != 2 {
		t.Fatalf("unexpected results: %#v", first)
	}
	first[0].ID = "CHANGED"
	if entries[0].ID == "CHANGED" || entries[1].ID == "CHANGED" {
		t.Fatalf("output aliases input: %#v", entries)
	}
}

// TestBankRecallStemmingOptInAndByteForByte 证明默认 Recall 路径逐字节不变，
// 且 opt-in 的 RecallStemming 只读、不改写磁盘日志。
func TestBankRecallStemmingOptInAndByteForByte(t *testing.T) {
	dir := t.TempDir()
	bank := NewBank(dir)
	id, err := bank.Retain("restore from the nightly backup", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, memoryFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// 默认关闭：backups 召不回 backup，且磁盘字节不变。
	if got := bank.Recall("backups", nil, 10); len(got) != 0 {
		t.Fatalf("default Recall should not stem: %#v", got)
	}
	mid, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, mid) {
		t.Fatalf("default Recall changed bank bytes: err=%v", err)
	}

	// 显式 opt-in：能召回，且同样不改写磁盘。
	got := bank.RecallStemming("backups", nil, 10)
	if len(got) != 1 || got[0].ID != id {
		t.Fatalf("RecallStemming = %#v, want [%s]", got, id)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("RecallStemming changed bank bytes: err=%v", err)
	}
}

// TestBankRecallStemmingRankingAndLimit 验证 opt-in 路径的排序与裁剪，
// 并确认作废条目被排除（与 Bank.Recall 的可见性一致）。
func TestBankRecallStemmingRankingAndLimit(t *testing.T) {
	bank := NewBank(t.TempDir())
	if _, err := bank.Retain("alpha backup", nil, ""); err != nil {
		t.Fatal(err)
	}
	id2, err := bank.Retain("alpha beta backup", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	got := bank.RecallStemming("alphas backups betas", nil, 10)
	if len(got) != 2 || got[0].ID != id2 {
		t.Fatalf("RecallStemming ranking = %#v, want id2 first", got)
	}
	if got = bank.RecallStemming("alphas backups betas", nil, 1); len(got) != 1 || got[0].ID != id2 {
		t.Fatalf("RecallStemming limit = %#v, want only id2", got)
	}

	if err := bank.Invalidate(id2, "stale"); err != nil {
		t.Fatal(err)
	}
	if got = bank.RecallStemming("alphas backups betas", nil, 10); len(got) != 1 || got[0].Text != "alpha backup" {
		t.Fatalf("invalidated entry leaked into stemming recall: %#v", got)
	}
}
