package skills

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestIndexTextClipsOverlongDescriptionGraphemeSafe 锁定字段级 rune 上限：
// 199 个 'a' 之后紧跟一个 ZWJ 家庭 emoji（7 个 rune），第 200 个 rune 必然落在
// emoji 字簇内部。裁剪必须回退到簇边界，既不切坏 UTF-8，也不留下半个 emoji。
func TestIndexTextClipsOverlongDescriptionGraphemeSafe(t *testing.T) {
	const family = "👨‍👩‍👧‍👦"
	desc := strings.Repeat("a", 199) + family + strings.Repeat("b", 50)
	r := New()
	r.Add(Skill{Name: "x", Description: desc})

	index := r.IndexText()
	if !utf8.ValidString(index) {
		t.Fatalf("index is not valid UTF-8: %q", index)
	}
	lines := strings.Split(strings.TrimSuffix(index, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("index has %d lines, want header + 1: %q", len(lines), index)
	}
	got := strings.TrimPrefix(lines[1], "- x: ")
	if want := strings.Repeat("a", 199); got != want {
		t.Errorf("clipped description = %q (%d runes), want %q", got, utf8.RuneCountInString(got), want)
	}
	if strings.Contains(index, "👨") {
		t.Errorf("partial emoji leaked into index: %q", index)
	}
}

// TestIndexTextClipsOverlongCJKDescription 验证纯 CJK 字段被裁到恰好
// skillIndexFieldRunes 个 rune，且仍是合法 UTF-8 与单行。
func TestIndexTextClipsOverlongCJKDescription(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "x", Description: strings.Repeat("汉", skillIndexFieldRunes+50)})

	index := r.IndexText()
	if !utf8.ValidString(index) {
		t.Fatalf("index is not valid UTF-8: %q", index)
	}
	lines := strings.Split(strings.TrimSuffix(index, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("index has %d lines, want header + 1: %q", len(lines), index)
	}
	got := strings.TrimPrefix(lines[1], "- x: ")
	if n := utf8.RuneCountInString(got); n != skillIndexFieldRunes {
		t.Errorf("clipped description has %d runes, want %d", n, skillIndexFieldRunes)
	}
}

// TestIndexTextTotalCapKeepsHeaderAndWholeLines 锁定索引总上限：超预算时按整行
// 截断，头部必须保留，被保留的每个技能仍是一行一条，且总长度不越界。
func TestIndexTextTotalCapKeepsHeaderAndWholeLines(t *testing.T) {
	r := New()
	for i := 0; i < 40; i++ {
		r.Add(Skill{
			Name:        fmt.Sprintf("skill-%02d", i),
			Description: strings.Repeat("d", skillIndexFieldRunes),
		})
	}
	index := r.IndexText()
	if !strings.HasPrefix(index, "Skills: procedures you can load by name") {
		t.Fatalf("header was dropped: %q", index)
	}
	if n := utf8.RuneCountInString(index); n > skillIndexTotalRunes {
		t.Errorf("index has %d runes, cap is %d", n, skillIndexTotalRunes)
	}
	lines := strings.Split(strings.TrimSuffix(index, "\n"), "\n")
	if len(lines) >= r.Len()+1 {
		t.Fatalf("index was not truncated: %d lines for %d skills", len(lines), r.Len())
	}
	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, "- ") {
			t.Errorf("line %d is not a whole skill row: %q", i, line)
		}
	}
	if !strings.HasSuffix(index, "\n") {
		t.Errorf("last skill row lost its trailing newline: %q", index)
	}
}

// TestLoadHeaderSanitizesNameAndPath 验证抬头里 name/path 与索引走同一条脱敏链：
// bidi 变成可见转义、tag 被剥离、换行折叠为单行，输出逐字节锁定。
func TestLoadHeaderSanitizesNameAndPath(t *testing.T) {
	const bidi = "\u200e"
	const tag = "\U000E0001"
	rawName := "docs" + bidi + "\n- fake " + tag
	rawPath := "/tmp/" + tag + "docs" + bidi + "\n/SKILL.md"
	r := New()
	r.Add(Skill{Name: rawName, Path: rawPath, Body: "body"})

	call, err := json.Marshal(map[string]string{"name": rawName})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.HandleUseSkill(call)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := "# skill: docs\\u200e - fake\n(source: /tmp/docs\\u200e /SKILL.md)\n\nbody"
	if out != want {
		t.Errorf("header = %q, want %q", out, want)
	}
	if strings.Contains(out, bidi) {
		t.Errorf("raw bidi character leaked into header: %q", out)
	}
	if strings.Contains(out, tag) {
		t.Errorf("raw tag character leaked into header: %q", out)
	}
	if !utf8.ValidString(out) {
		t.Errorf("header is not valid UTF-8: %q", out)
	}
}

// TestIndexAndLoadHeaderNameStayConsistent 锁定“索引名 == 抬头名”：
// 含 bidi、tag、换行与超长尾部的名称在两处渲染结果必须逐字节一致。
func TestIndexAndLoadHeaderNameStayConsistent(t *testing.T) {
	const bidi = "\u200e"
	const tag = "\U000E0001"
	rawName := "skill" + bidi + "\n" + tag + strings.Repeat("长", skillIndexFieldRunes+10)
	r := New()
	r.Add(Skill{Name: rawName, Body: "body"})

	index := r.IndexText()
	indexLine := strings.Split(strings.TrimSuffix(index, "\n"), "\n")[1]
	indexName := strings.TrimPrefix(indexLine, "- ")

	call, err := json.Marshal(map[string]string{"name": rawName})
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.HandleUseSkill(call)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	headerLine := strings.SplitN(out, "\n", 2)[0]
	headerName := strings.TrimPrefix(headerLine, "# skill: ")

	if headerName != indexName {
		t.Errorf("header name %q != index name %q", headerName, indexName)
	}
	if indexName == rawName {
		t.Error("name was not sanitized")
	}
	if strings.Contains(indexName, "\n") || strings.Contains(indexName, bidi) || strings.Contains(indexName, tag) {
		t.Errorf("index name still dirty: %q", indexName)
	}
	if n := utf8.RuneCountInString(indexName); n > skillIndexFieldRunes {
		t.Errorf("name not clipped: %d runes", n)
	}
}

// TestLoadHeaderNormalSkillByteStable 验证正常技能（短字段）的抬头完全不变，
// 延续默认帧逐字节稳定的契约。
func TestLoadHeaderNormalSkillByteStable(t *testing.T) {
	r := New()
	r.Add(Skill{Name: "deploy", Description: "ship it", Path: "/tmp/deploy/SKILL.md", Body: "body of deploy"})
	out, err := r.HandleUseSkill(json.RawMessage(`{"name":"deploy"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# skill: deploy\n(source: /tmp/deploy/SKILL.md)\n\nbody of deploy"
	if out != want {
		t.Errorf("header = %q, want %q", out, want)
	}
}
