package agentboard_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/model"
)

func sampleState() *model.State {
	st := model.NewState()
	st.Projects["AB"] = &model.Project{Key: "AB", Name: "Agent Board", NextSeq: 1}
	st.Tasks["AB-1"] = &model.Task{ID: "AB-1", Project: "AB", Type: model.KindTask, Title: "t", Status: model.StatusTodo, Priority: model.PriorityLow, Labels: []string{"x"}}
	st.NextActivityID = 7
	return st
}

func TestStores(t *testing.T) {
	stores := map[string]func(t *testing.T) agentboard.Store{
		"mem": func(*testing.T) agentboard.Store { return agentboard.NewMemStore() },
		"file": func(t *testing.T) agentboard.Store {
			return agentboard.NewFileStore(filepath.Join(t.TempDir(), "s", "board.json"))
		},
	}
	for name, mk := range stores {
		t.Run(name+"/empty load", func(t *testing.T) {
			st, err := mk(t).Load()
			if err != nil {
				t.Fatal(err)
			}
			if st.Projects == nil || st.Tasks == nil || st.Agents == nil {
				t.Fatalf("maps must be non-nil: %+v", st)
			}
		})
		t.Run(name+"/round trip", func(t *testing.T) {
			s := mk(t)
			if err := s.Save(sampleState()); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.Tasks["AB-1"].Title != "t" || got.NextActivityID != 7 || got.Projects["AB"].NextSeq != 1 {
				t.Fatalf("got %+v", got)
			}
		})
		t.Run(name+"/no aliasing", func(t *testing.T) {
			s := mk(t)
			st := sampleState()
			s.Save(st)
			st.Tasks["AB-1"].Title = "mutated after save"
			got, _ := s.Load()
			if got.Tasks["AB-1"].Title != "t" {
				t.Fatal("store aliases caller memory")
			}
		})
	}
}

func TestFileStoreLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := agentboard.NewFileStore(filepath.Join(dir, "board.json"))
	for i := 0; i < 5; i++ {
		if err := s.Save(sampleState()); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "board.json" {
		t.Fatalf("directory has %v", entries)
	}
}

func TestFileStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	_, err := agentboard.NewFileStore(path).Load()
	if err == nil || !strings.Contains(err.Error(), "board.json") {
		t.Fatalf("err = %v", err)
	}
	if _, err := agentboard.Open(agentboard.Options{Store: agentboard.NewFileStore(path)}); err == nil {
		t.Fatal("Open must fail on a corrupt store")
	}
}

func TestFileStoreNullMaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	os.WriteFile(path, []byte(`{"projects":null,"tasks":null,"agents":null}`), 0o600)
	st, err := agentboard.NewFileStore(path).Load()
	if err != nil || st.Tasks == nil || st.Projects == nil || st.Agents == nil {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}
