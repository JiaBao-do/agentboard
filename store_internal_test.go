package agentboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard/model"
)

// A crash between writing the temporary file and renaming it must leave the
// previous file intact and loadable, and a stray temp file must not confuse
// the next start.
func TestFileStoreCrashBeforeRenameKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.json")
	fs := NewFileStore(path)

	old := model.NewState()
	old.Projects["OLD"] = &model.Project{Key: "OLD", Name: "old"}
	if err := fs.Save(old); err != nil {
		t.Fatal(err)
	}

	fs.beforeRename = func() error { return errors.New("power cut") }
	next := model.NewState()
	next.Projects["NEW"] = &model.Project{Key: "NEW", Name: "new"}
	if err := fs.Save(next); err == nil {
		t.Fatal("expected the simulated crash to surface")
	}
	fs.beforeRename = nil

	got, err := fs.Load()
	if err != nil {
		t.Fatalf("file corrupted by a crash before rename: %v", err)
	}
	if got.Projects["OLD"] == nil || got.Projects["NEW"] != nil {
		t.Fatalf("loaded %+v, want the previous state", got.Projects)
	}

	// A real crash cannot run the deferred cleanup: leave junk behind.
	if err := os.WriteFile(filepath.Join(dir, "board.json.123.tmp"), []byte("{half a fi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := NewFileStore(path).Load(); err != nil || got.Projects["OLD"] == nil {
		t.Fatalf("stray temp file broke loading: %v", err)
	}
	// And the next save still works and replaces the file atomically.
	if err := fs.Save(next); err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.Load(); got.Projects["NEW"] == nil {
		t.Fatal("save after crash did not take effect")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") && e.Name() != "board.json.123.tmp" {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}
