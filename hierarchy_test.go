package agentboard_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard"
	"github.com/JiaBao-do/agentboard/model"
)

func TestHierarchy(t *testing.T) {
	newTree := func(t *testing.T) *agentboard.Board {
		b, _ := newBoard(t)
		must := func(in agentboard.NewTask) {
			t.Helper()
			if _, err := b.AddTask(in); err != nil {
				t.Fatal(err)
			}
		}
		must(agentboard.NewTask{Project: "AB", Type: agentboard.KindEpic, Title: "epic"})                   // AB-1
		must(agentboard.NewTask{Project: "AB", Type: agentboard.KindStory, Title: "story", Parent: "AB-1"}) // AB-2
		must(agentboard.NewTask{Project: "AB", Title: "task", Parent: "AB-2"})                              // AB-3
		return b
	}
	tests := []struct {
		name    string
		in      agentboard.NewTask
		wantErr error
	}{
		{"epic without parent", agentboard.NewTask{Project: "AB", Type: agentboard.KindEpic, Title: "e"}, nil},
		{"story under epic", agentboard.NewTask{Project: "AB", Type: agentboard.KindStory, Title: "s", Parent: "AB-1"}, nil},
		{"task under story", agentboard.NewTask{Project: "AB", Title: "t", Parent: "AB-2"}, nil},
		{"task directly under epic", agentboard.NewTask{Project: "AB", Title: "t", Parent: "AB-1"}, nil},
		{"epic under story", agentboard.NewTask{Project: "AB", Type: agentboard.KindEpic, Title: "e", Parent: "AB-2"}, agentboard.ErrInvalid},
		{"story under story", agentboard.NewTask{Project: "AB", Type: agentboard.KindStory, Title: "s", Parent: "AB-2"}, agentboard.ErrInvalid},
		{"task under task", agentboard.NewTask{Project: "AB", Title: "t", Parent: "AB-3"}, agentboard.ErrInvalid},
		{"unknown parent", agentboard.NewTask{Project: "AB", Title: "t", Parent: "AB-99"}, agentboard.ErrNotFound},
		{"unknown type", agentboard.NewTask{Project: "AB", Type: "bug", Title: "t"}, agentboard.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newTree(t)
			_, err := b.AddTask(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
	t.Run("parent must be in the same project", func(t *testing.T) {
		b := newTree(t)
		b.CreateProject("ZZ", "Other", "")
		_, err := b.AddTask(agentboard.NewTask{Project: "ZZ", Title: "t", Parent: "AB-2"})
		if !errors.Is(err, agentboard.ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("reparent and detach", func(t *testing.T) {
		b := newTree(t)
		empty, epic := "", "AB-1"
		got, err := b.Update("AB-3", agentboard.Patch{Parent: &empty})
		if err != nil || got.Parent != "" {
			t.Fatalf("detach: %+v %v", got, err)
		}
		got, err = b.Update("AB-3", agentboard.Patch{Parent: &epic})
		if err != nil || got.Parent != "AB-1" {
			t.Fatalf("reparent: %+v %v", got, err)
		}
		self := "AB-3"
		if _, err := b.Update("AB-3", agentboard.Patch{Parent: &self}); !errors.Is(err, agentboard.ErrInvalid) {
			t.Fatalf("self parent: %v", err)
		}
		bad := "AB-3"
		if _, err := b.Update("AB-1", agentboard.Patch{Parent: &bad}); !errors.Is(err, agentboard.ErrInvalid) {
			t.Fatalf("epic under task: %v", err)
		}
	})
	t.Run("filters", func(t *testing.T) {
		b := newTree(t)
		if n := len(b.Tasks(agentboard.Filter{Type: agentboard.KindStory})); n != 1 {
			t.Fatalf("type filter: %d", n)
		}
		if n := len(b.Tasks(agentboard.Filter{Parent: "AB-2"})); n != 1 {
			t.Fatalf("parent filter: %d", n)
		}
	})
}

func TestHeartbeatMeta(t *testing.T) {
	b, _ := newBoard(t)
	a, err := b.Heartbeat("alice", agentboard.HeartbeatRequest{Meta: map[string]string{"model": "x", "host": "h1"}})
	if err != nil || a.Meta["model"] != "x" {
		t.Fatalf("a=%+v err=%v", a, err)
	}
	a.Meta["model"] = "mutated" // must not alias the stored copy
	if got := b.Agents()[0].Meta["model"]; got != "x" {
		t.Fatalf("meta aliased: %q", got)
	}
	// nil Meta keeps what is stored; a non-nil map replaces it.
	b.Heartbeat("alice", agentboard.HeartbeatRequest{})
	if b.Agents()[0].Meta["host"] != "h1" {
		t.Fatal("nil meta should keep the stored one")
	}
	b.Heartbeat("alice", agentboard.HeartbeatRequest{Meta: map[string]string{"only": "this"}})
	if m := b.Agents()[0].Meta; len(m) != 1 || m["only"] != "this" {
		t.Fatalf("meta = %v", m)
	}

	tooMany := map[string]string{}
	for i := 0; i < 17; i++ {
		tooMany[strings.Repeat("k", i+1)] = "v"
	}
	for name, m := range map[string]map[string]string{
		"too many keys": tooMany,
		"empty key":     {"": "v"},
		"long key":      {strings.Repeat("k", 33): "v"},
		"long value":    {"k": strings.Repeat("v", 257)},
	} {
		if _, err := b.Heartbeat("alice", agentboard.HeartbeatRequest{Meta: m}); !errors.Is(err, agentboard.ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestExportRoundTrip(t *testing.T) {
	b, c := newBoard(t)
	addTask(t, b, "one")
	b.Claim("AB-1", "alice", time.Hour)
	b.Comment("AB-1", "alice", "note")
	c.Advance(time.Second)

	st, err := b.Export()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := agentboard.EncodeState(&buf, st); err != nil {
		t.Fatal(err)
	}
	back, err := agentboard.DecodeState(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if back.Version != model.SchemaVersion || back.Tasks["AB-1"].Assignee != "alice" || len(back.Activity) != 4 {
		t.Fatalf("round trip lost data: %+v", back)
	}
	// The export is a deep copy: mutating it must not touch the board.
	st.Tasks["AB-1"].Title = "mutated"
	if d, _ := b.Task("AB-1"); d.Task.Title != "one" {
		t.Fatal("export aliases live state")
	}
}

func TestShutdownMarksAgentsOfflineAndLogs(t *testing.T) {
	b, _ := newBoard(t)
	addTask(t, b, "job")
	b.Claim("AB-1", "alice", time.Hour)
	if !b.Agents()[0].Online {
		t.Fatal("alice should be online before shutdown")
	}
	ch, cancel := b.Subscribe()
	defer cancel()
	if err := b.Shutdown("admin"); err != nil {
		t.Fatal(err)
	}
	if ev := <-ch; ev.Type != "shutdown" {
		t.Fatalf("event = %+v", ev)
	}
	a := b.Agents()[0]
	if a.Online || a.CurrentTask != "" {
		t.Fatalf("agent after shutdown: %+v", a)
	}
	if d, _ := b.Task("AB-1"); d.Task.Assignee != "alice" || d.Task.Status != agentboard.StatusInProgress {
		t.Fatalf("leases must survive a restart: %+v", d.Task)
	}
	if last := b.Recent(1)[0]; last.Action != "server_stopped" || last.Actor != "admin" {
		t.Fatalf("last activity = %+v", last)
	}
	// An agent that heartbeats after the restart is online again.
	if _, err := b.Heartbeat("alice", agentboard.HeartbeatRequest{Task: "AB-1"}); err != nil {
		t.Fatal(err)
	}
	if !b.Agents()[0].Online {
		t.Fatal("alice should be back online")
	}
}

func TestDecodeStateVersioningAndValidation(t *testing.T) {
	base := func(mut string) string {
		return `{"version":1,"projects":{"AB":{"key":"AB","name":"n","next_seq":1}},` +
			`"tasks":{"AB-1":{"id":"AB-1","project":"AB","type":"task","title":"t","status":"todo","priority":"low"` + mut + `}},` +
			`"agents":{},"activity":[],"next_activity_id":0}`
	}
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"valid", base(""), ""},
		{"empty input", "", ""},
		{"legacy without version and type", `{"projects":{"AB":{"key":"AB","name":"n","next_seq":1}},"tasks":{"AB-1":{"id":"AB-1","project":"AB","title":"t","status":"todo","priority":"low"}}}`, ""},
		{"newer schema", `{"version":99}`, "schema version 99"},
		{"negative version", `{"version":-1}`, "negative"},
		{"bad json", `{`, "unexpected"},
		{"unknown project", strings.Replace(base(""), `"project":"AB"`, `"project":"NOPE"`, 1), "unknown project"},
		{"bad status", strings.Replace(base(""), `"status":"todo"`, `"status":"weird"`, 1), "invalid status"},
		{"bad priority", strings.Replace(base(""), `"priority":"low"`, `"priority":"weird"`, 1), "invalid priority"},
		{"bad type", strings.Replace(base(""), `"type":"task"`, `"type":"bug"`, 1), "invalid type"},
		{"unknown parent", base(`,"parent":"AB-9"`), "unknown parent"},
		{"self parent", base(`,"parent":"AB-1"`), "cannot have parent"},
		{"seq behind", strings.Replace(base(""), `"next_seq":1`, `"next_seq":0`, 1), "behind"},
		{"key mismatch", strings.Replace(base(""), `"id":"AB-1"`, `"id":"AB-2"`, 1), "does not match"},
		{"activity order", strings.Replace(base(""), `"activity":[]`, `"activity":[{"id":2},{"id":1}]`, 1), "strictly increase"},
		{"activity counter", strings.Replace(base(""), `"activity":[]`, `"activity":[{"id":5}]`, 1), "next_activity_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := agentboard.DecodeState([]byte(tc.data))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if st.Version != model.SchemaVersion {
					t.Fatalf("version = %d, want migrated to %d", st.Version, model.SchemaVersion)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
	t.Run("legacy tasks get a type", func(t *testing.T) {
		st, err := agentboard.DecodeState([]byte(`{"projects":{"AB":{"key":"AB","name":"n","next_seq":1}},"tasks":{"AB-1":{"id":"AB-1","project":"AB","title":"t","status":"todo","priority":"low"}}}`))
		if err != nil || st.Tasks["AB-1"].Type != model.KindTask {
			t.Fatalf("st=%+v err=%v", st, err)
		}
	})
}
