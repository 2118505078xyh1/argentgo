package session

import (
	"os"
	"testing"
)

func TestSessionSyncFlushesExistingFile(t *testing.T) {
	cases := []struct {
		name       string
		append     bool
		removeFile bool
		wantErr    bool
	}{
		{name: "fresh session"},
		{name: "after appends", append: true},
		{name: "missing file reports error", removeFile: true, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newTestSession(t, t.TempDir(), "sync")
			if tc.append {
				appendAll(t, sess, KindUser, KindAssistant, KindTool)
			}
			if tc.removeFile {
				if err := os.Remove(sess.path()); err != nil {
					t.Fatalf("remove session file: %v", err)
				}
			}

			err := sess.Sync()
			if tc.wantErr {
				if err == nil {
					t.Fatal("Sync succeeded for a missing file")
				}
				return
			}
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			// Sync 必须可重复，且不得改变转录。
			before, err := sess.Records()
			if err != nil {
				t.Fatalf("Records: %v", err)
			}
			if err := sess.Sync(); err != nil {
				t.Fatalf("second Sync: %v", err)
			}
			after, err := sess.Records()
			if err != nil {
				t.Fatalf("Records after Sync: %v", err)
			}
			if len(before) != len(after) {
				t.Fatalf("Sync changed record count: %d -> %d", len(before), len(after))
			}
		})
	}
}
