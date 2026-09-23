package agentboard_test

import (
	"strings"
	"testing"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/model"
)

// TestDecodeStateMigratesV1BoardsWithoutUsers checks the schema version 1 ->
// 2 migration added for User accounts: a genuine pre-User board file (no
// "users" key at all, as every real file saved before this change looks)
// must decode to schema version 2 with a non-nil, empty Users map, not be
// rejected and not panic on a nil map.
func TestDecodeStateMigratesV1BoardsWithoutUsers(t *testing.T) {
	const v1 = `{"version":1,"projects":{"AB":{"key":"AB","name":"n","next_seq":1}},` +
		`"tasks":{"AB-1":{"id":"AB-1","project":"AB","type":"task","title":"t","status":"todo","priority":"low"}},` +
		`"agents":{},"activity":[],"next_activity_id":0}`

	st, err := agentboard.DecodeState([]byte(v1))
	if err != nil {
		t.Fatalf("DecodeState: %v", err)
	}
	if st.Version != model.SchemaVersion {
		t.Fatalf("version = %d, want migrated to %d", st.Version, model.SchemaVersion)
	}
	if st.Users == nil {
		t.Fatal("Users is nil after migrating a v1 board; want a non-nil empty map")
	}
	if len(st.Users) != 0 {
		t.Fatalf("Users = %v, want empty", st.Users)
	}

	// The migrated state must itself still validate and round-trip through
	// EncodeState/DecodeState (an operator running "export" right after an
	// upgrade must get valid output).
	if err := agentboard.ValidateState(st); err != nil {
		t.Fatalf("migrated state does not validate: %v", err)
	}
	var buf strings.Builder
	if err := agentboard.EncodeState(&buf, st); err != nil {
		t.Fatalf("EncodeState: %v", err)
	}
	if !strings.Contains(buf.String(), `"users"`) {
		t.Fatal("re-encoded state is missing the users field")
	}
	back, err := agentboard.DecodeState([]byte(buf.String()))
	if err != nil {
		t.Fatalf("DecodeState of re-encoded state: %v", err)
	}
	if back.Version != model.SchemaVersion || len(back.Users) != 0 {
		t.Fatalf("round trip changed shape: %+v", back)
	}
}

// TestDecodeStateWithGenuineUsers checks that a well-formed users map
// decodes and validates, and that FileStore.Save/Load (EncodeFile/DecodeFile)
// preserves credential bytes exactly - a corrupted salt or hash would lock
// every user out.
func TestFileStoreRoundTripsUsers(t *testing.T) {
	st := model.NewState()
	st.Projects["AB"] = &model.Project{Key: "AB", Name: "n", NextSeq: 0}
	st.Users["dev@example.com"] = &model.User{
		Email:        "dev@example.com",
		PasswordHash: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		Salt:         []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0, 1, 2, 3, 4, 5, 6},
		Iterations:   600_000,
	}
	if err := agentboard.ValidateState(st); err != nil {
		t.Fatalf("ValidateState: %v", err)
	}

	dir := t.TempDir()
	fs := agentboard.NewFileStore(dir + "/board.json")
	if err := fs.Save(st); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := fs.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := back.Users["dev@example.com"]
	if got == nil {
		t.Fatal("user missing after round trip")
	}
	want := st.Users["dev@example.com"]
	if string(got.PasswordHash) != string(want.PasswordHash) || string(got.Salt) != string(want.Salt) || got.Iterations != want.Iterations {
		t.Fatalf("credential bytes changed across a save/load round trip: got %+v, want %+v", got, want)
	}
}

// TestValidateStateRejectsBadUsers checks the invariants ValidateState now
// enforces on Users: the map key must match the user's own (lower case)
// email, and a user record needs a hash, salt and iteration count - an
// import that skipped hashing (or hand-edited the export) must not corrupt
// the board into accounts nobody can log in to, or worse, an account
// anyone can log in to.
func TestValidateStateRejectsBadUsers(t *testing.T) {
	valid := func() *model.State {
		st := model.NewState()
		st.Users["dev@example.com"] = &model.User{
			Email: "dev@example.com", PasswordHash: []byte{1, 2, 3}, Salt: []byte{4, 5, 6}, Iterations: 1000,
		}
		return st
	}
	if err := agentboard.ValidateState(valid()); err != nil {
		t.Fatalf("well-formed users map rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*model.State)
	}{
		{"key/email mismatch", func(st *model.State) { st.Users["dev@example.com"].Email = "other@example.com" }},
		{"upper case key", func(st *model.State) {
			u := st.Users["dev@example.com"]
			delete(st.Users, "dev@example.com")
			u.Email = "DEV@example.com"
			st.Users["DEV@example.com"] = u
		}},
		{"missing hash", func(st *model.State) { st.Users["dev@example.com"].PasswordHash = nil }},
		{"missing salt", func(st *model.State) { st.Users["dev@example.com"].Salt = nil }},
		{"zero iterations", func(st *model.State) { st.Users["dev@example.com"].Iterations = 0 }},
		{"nil user pointer", func(st *model.State) { st.Users["dev@example.com"] = nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := valid()
			c.mut(st)
			if err := agentboard.ValidateState(st); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}
