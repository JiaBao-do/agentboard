package agentboard_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/model"
)

func archiveBoard(t *testing.T, dir string, c *clock, max int) *agentboard.Board {
	t.Helper()
	b, err := agentboard.Open(agentboard.Options{
		Store: agentboard.NewFileStore(filepath.Join(dir, "board.json")), Now: c.Now,
		ArchiveDir: filepath.Join(dir, "archive"), MaxActivity: max,
		SaveDebounce: time.Millisecond, SaveMaxLatency: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestActivityIsArchivedAndNothingIsLost(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t: time.Date(2026, 1, 30, 12, 0, 0, 0, time.UTC)}
	b := archiveBoard(t, dir, c, 20)
	b.CreateProject("AB", "n", "tester")
	b.AddTask(agentboard.NewTask{Actor: "tester", Project: "AB", Title: "t"})
	for i := range 100 {
		if i == 40 {
			c.Advance(72 * time.Hour) // crosses into February: a second monthly file
		}
		b.Comment("AB-1", "tester", "c")
	}
	if err := b.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	b2 := archiveBoard(t, dir, c, 20)
	defer b2.Close(context.Background())
	live := b2.Recent(1000)
	if len(live) > 21 {
		t.Fatalf("live log has %d entries, expected it trimmed near 20", len(live))
	}
	arch, err := agentboard.ReadArchive(filepath.Join(dir, "archive"))
	if err != nil || len(arch) == 0 {
		t.Fatalf("archive: %d entries, err=%v", len(arch), err)
	}
	// Archived + live is the complete, gap-free history: 1 project + 1 task + 100 comments.
	st, _ := b2.Export()
	all := agentboard.MergeActivity(arch, st.Activity)
	if len(all) != 102 {
		t.Fatalf("history has %d entries, want 102", len(all))
	}
	for i, a := range all {
		if a.ID != int64(i+1) {
			t.Fatalf("gap or reorder at %d: id %d", i, a.ID)
		}
	}
	for _, name := range []string{"activity-202601.jsonl.gz", "activity-202602.jsonl.gz"} {
		if _, err := os.Stat(filepath.Join(dir, "archive", name)); err != nil {
			t.Errorf("missing monthly archive %s", name)
		}
	}
	if st.ArchivedThrough == 0 {
		t.Error("ArchivedThrough not recorded")
	}
	if info, _ := os.Stat(filepath.Join(dir, "board.json")); info.Size() > 4000 {
		t.Errorf("data file is %d bytes despite archiving", info.Size())
	}
}

func TestArchiveFailureKeepsEverythingAndSurfaces(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "archive"), []byte("i am a file, not a directory"), 0o600)
	c := &clock{t: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}
	b, _ := agentboard.Open(agentboard.Options{
		Store: agentboard.NewFileStore(filepath.Join(dir, "board.json")), Now: c.Now,
		ArchiveDir: filepath.Join(dir, "archive"), MaxActivity: 5,
		SaveDebounce: time.Hour, SaveMaxLatency: 2 * time.Hour,
	})
	b.CreateProject("AB", "n", "tester")
	b.AddTask(agentboard.NewTask{Actor: "tester", Project: "AB", Title: "t"})
	for range 20 {
		b.Comment("AB-1", "tester", "c")
	}
	if err := b.Flush(context.Background()); err == nil {
		t.Fatal("archive failure must surface")
	}
	if s := b.SaveStatus(); s.LastError == "" || s.State() != "failed" {
		t.Fatalf("status = %+v", s)
	}
	if n := len(b.Recent(1000)); n != 22 {
		t.Fatalf("entries were dropped despite the failed archive: %d", n)
	}
}

func TestReadArchiveDedupesAndRefusesNewerVersions(t *testing.T) {
	dir := t.TempDir()
	member := func(header string, ids ...int) []byte {
		var b bytes.Buffer
		zw := gzip.NewWriter(&b)
		zw.Write([]byte(header + "\n"))
		for _, id := range ids {
			zw.Write([]byte(`{"id":` + itoa(id) + `,"time":"2026-01-01T00:00:00Z","actor":"a","action":"x"}` + "\n"))
		}
		zw.Close()
		return b.Bytes()
	}
	// Two appended gzip members with an overlap (a crash re-archived 2 and 3).
	body := append(member(`{"agentboard_archive":1}`, 1, 2, 3), member(`{"agentboard_archive":1}`, 2, 3, 4)...)
	os.WriteFile(filepath.Join(dir, "activity-202601.jsonl.gz"), body, 0o600)
	got, err := agentboard.ReadArchive(dir)
	if err != nil || len(got) != 4 || got[3].ID != 4 {
		t.Fatalf("got %d entries, err=%v", len(got), err)
	}
	os.WriteFile(filepath.Join(dir, "activity-202602.jsonl.gz"), member(`{"agentboard_archive":9}`, 9), 0o600)
	if _, err := agentboard.ReadArchive(dir); !errors.Is(err, agentboard.ErrInvalid) {
		t.Fatalf("newer archive version: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "activity-202602.jsonl.gz"), []byte("not gzip"), 0o600)
	if _, err := agentboard.ReadArchive(dir); !errors.Is(err, agentboard.ErrCorrupt) {
		t.Fatalf("corrupt archive: %v", err)
	}
	if got, err := agentboard.ReadArchive(filepath.Join(dir, "nope")); err != nil || len(got) != 0 {
		t.Fatalf("missing dir must be an empty archive: %v %v", got, err)
	}
}

func TestMergeActivity(t *testing.T) {
	a := []model.Activity{{ID: 3}, {ID: 1}}
	b := []model.Activity{{ID: 3}, {ID: 5}, {ID: 2}}
	var ids []string
	for _, x := range agentboard.MergeActivity(a, b) {
		ids = append(ids, itoa(int(x.ID)))
	}
	if strings.Join(ids, ",") != "1,2,3,5" {
		t.Fatalf("merged = %v", ids)
	}
}
