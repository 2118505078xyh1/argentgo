package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestImageOffloadProjection 验证图像按请求顺序标记最旧 occurrence，
// Messages() 投影把这些图像 part 替换为有界 [image omitted] 占位，旧转录字节不变。
func TestImageOffloadProjection(t *testing.T) {
	dir := t.TempDir()
	id := "image-offload"
	path := filepath.Join(dir, id+fileExt)
	ts := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	imagePart := `{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}`
	first := json.RawMessage(`{"content":[{"type":"text","text":"first"},` + imagePart + `,` + imagePart + `]}`)
	second := json.RawMessage(`{"content":[{"type":"text","text":"second"},` + imagePart + `]}`)
	records := []Record{
		{ID: "n000001", Kind: KindHeader, TS: ts, Data: json.RawMessage(`{"title":"t","created":"2026-11-23T10:00:00Z"}`)},
		{ID: "n000002", Parent: "n000001", Kind: KindUser, TS: ts, Data: first},
		{ID: "n000003", Parent: "n000002", Kind: KindAssistant, TS: ts, Data: json.RawMessage(`{"content":[{"type":"image","source":{"data":"AAAA"}}]}`)},
		{ID: "n000004", Parent: "n000003", Kind: KindTool, TS: ts, Data: second},
	}
	marker, err := ImageOffloadRecord(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	marker.ID, marker.Parent, marker.TS = "n000005", "n000004", ts
	records = append(records, marker)
	var buf strings.Builder
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSession(dir, id)
	if err != nil {
		t.Fatal(err)
	}
	messages := s.Messages()
	if len(messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, tool)", len(messages))
	}
	firstData := string(messages[0].Data)
	if strings.Count(firstData, ImageOmittedPlaceholder) != 1 {
		t.Fatalf("first message did not offload exactly one image: %s", firstData)
	}
	if strings.Count(firstData, "data:image/png") != 1 {
		t.Fatalf("second image in first message was lost: %s", firstData)
	}
	if strings.Count(string(messages[2].Data), "data:image/png") != 1 {
		t.Fatalf("tool image should be untouched: %s", messages[2].Data)
	}

	// 规划：按请求顺序只标最旧的、尚未卸载的 occurrence，跳过 assistant。
	planned := PlanImageOffload(messages, []ImageOffload{{Seq: 1, Index: 0}}, 1)
	if len(planned) != 1 || planned[0].Index != 1 {
		t.Fatalf("PlanImageOffload = %+v, want index 1", planned)
	}
}
