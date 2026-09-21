package agentboard_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newBoard(t *testing.T) (*agentboard.Board, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	b, err := agentboard.Open(agentboard.Options{Now: c.Now, AgentTTL: time.Minute, Lease: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	if _, err := b.CreateProject("AB", "Agent Board", "tester"); err != nil {
		t.Fatal(err)
	}
	return b, c
}

func addTask(t *testing.T, b *agentboard.Board, title string) agentboard.Task {
	t.Helper()
	task, err := b.AddTask(agentboard.NewTask{Project: "AB", Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCreateProject(t *testing.T) {
	tests := []struct {
		name, key, title string
		wantErr          error
	}{
		{"ok", "WEB", "Website", nil},
		{"digits allowed", "A1", "Two chars", nil},
		{"duplicate", "AB", "Again", agentboard.ErrExists},
		{"lowercase key", "web", "Website", agentboard.ErrInvalid},
		{"too short", "A", "Website", agentboard.ErrInvalid},
		{"starts with digit", "1AB", "Website", agentboard.ErrInvalid},
		{"too long", "ABCDEFGHIJK", "Website", agentboard.ErrInvalid},
		{"empty name", "XYZ", "  ", agentboard.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newBoard(t)
			_, err := b.CreateProject(tc.key, tc.title, "")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestAddTask(t *testing.T) {
	tests := []struct {
		name    string
		in      agentboard.NewTask
		wantErr error
		check   func(*testing.T, agentboard.Task)
	}{
		{"defaults", agentboard.NewTask{Project: "AB", Title: " Fix it "}, nil, func(t *testing.T, k agentboard.Task) {
			if k.ID != "AB-1" || k.Title != "Fix it" || k.Status != agentboard.StatusTodo ||
				k.Priority != agentboard.PriorityMedium || k.CreatedBy != "anonymous" {
				t.Fatalf("unexpected task %+v", k)
			}
		}},
		{"labels normalised", agentboard.NewTask{Project: "AB", Title: "x", Labels: []string{" Bug ", "bug", "", "Go:1"}}, nil, func(t *testing.T, k agentboard.Task) {
			if strings.Join(k.Labels, ",") != "bug,go:1" {
				t.Fatalf("labels = %v", k.Labels)
			}
		}},
		{"unknown project", agentboard.NewTask{Project: "NOPE", Title: "x"}, agentboard.ErrNotFound, nil},
		{"empty title", agentboard.NewTask{Project: "AB", Title: " "}, agentboard.ErrInvalid, nil},
		{"long title", agentboard.NewTask{Project: "AB", Title: strings.Repeat("x", 201)}, agentboard.ErrInvalid, nil},
		{"bad priority", agentboard.NewTask{Project: "AB", Title: "x", Priority: "asap"}, agentboard.ErrInvalid, nil},
		{"bad label", agentboard.NewTask{Project: "AB", Title: "x", Labels: []string{"has space"}}, agentboard.ErrInvalid, nil},
		{"too many labels", agentboard.NewTask{Project: "AB", Title: "x", Labels: strings.Split("a,b,c,d,e,f,g,h,i,j,k", ",")}, agentboard.ErrInvalid, nil},
		{"bad actor", agentboard.NewTask{Project: "AB", Title: "x", Actor: "a b"}, agentboard.ErrInvalid, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newBoard(t)
			got, err := b.AddTask(tc.in)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && tc.check != nil {
				tc.check(t, got)
			}
		})
	}
}

func TestTaskIDsAreSequentialAndOrdered(t *testing.T) {
	b, _ := newBoard(t)
	for i := 0; i < 12; i++ {
		addTask(t, b, fmt.Sprint("t", i))
	}
	tasks := b.Tasks(agentboard.Filter{})
	if len(tasks) != 12 || tasks[1].ID != "AB-2" || tasks[9].ID != "AB-10" || tasks[11].ID != "AB-12" {
		t.Fatalf("bad order: %v ... %v", tasks[1].ID, tasks[9].ID)
	}
}

func TestClaim(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(b *agentboard.Board, c *clock)
		agent   string
		lease   time.Duration
		wantErr error
	}{
		{"first claim", nil, "alice", 0, nil},
		{"other agent while lease active", func(b *agentboard.Board, _ *clock) { b.Claim("AB-1", "bob", time.Minute) }, "alice", 0, agentboard.ErrClaimed},
		{"same agent renews", func(b *agentboard.Board, _ *clock) { b.Claim("AB-1", "alice", time.Minute) }, "alice", 0, nil},
		{"other agent after expiry", func(b *agentboard.Board, c *clock) {
			b.Claim("AB-1", "bob", time.Minute)
			c.Advance(time.Minute)
		}, "alice", 0, nil},
		{"other agent after release", func(b *agentboard.Board, _ *clock) {
			b.Claim("AB-1", "bob", time.Minute)
			b.Release("AB-1", "bob")
		}, "alice", 0, nil},
		{"done task", func(b *agentboard.Board, _ *clock) { b.Complete("AB-1", "bob") }, "alice", 0, agentboard.ErrInvalid},
		{"bad agent name", nil, "a b", 0, agentboard.ErrInvalid},
		{"lease too short", nil, "alice", time.Millisecond, agentboard.ErrInvalid},
		{"lease too long", nil, "alice", 48 * time.Hour, agentboard.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, c := newBoard(t)
			addTask(t, b, "job")
			if tc.setup != nil {
				tc.setup(b, c)
			}
			got, err := b.Claim("AB-1", tc.agent, tc.lease)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && (got.Assignee != tc.agent || got.Status != agentboard.StatusInProgress || got.LeaseExpires == nil) {
				t.Fatalf("unexpected task %+v", got)
			}
		})
	}
	t.Run("unknown task", func(t *testing.T) {
		b, _ := newBoard(t)
		if _, err := b.Claim("AB-9", "alice", 0); !errors.Is(err, agentboard.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLeaseExpiryReleasesTask(t *testing.T) {
	b, c := newBoard(t)
	addTask(t, b, "job")
	if _, err := b.Claim("AB-1", "alice", time.Minute); err != nil {
		t.Fatal(err)
	}
	c.Advance(59 * time.Second)
	if d, _ := b.Task("AB-1"); d.Task.Status != agentboard.StatusInProgress {
		t.Fatalf("released early: %+v", d.Task)
	}
	c.Advance(time.Second)
	d, err := b.Task("AB-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.Status != agentboard.StatusTodo || d.Task.Assignee != "" || d.Task.LeaseExpires != nil {
		t.Fatalf("not released: %+v", d.Task)
	}
	last := d.Activity[len(d.Activity)-1]
	if last.Action != "lease_expired" || last.Actor != "system" || !strings.Contains(last.Detail, "alice") {
		t.Fatalf("last activity = %+v", last)
	}
	for _, a := range b.Agents() {
		if a.Name == "alice" && a.CurrentTask != "" {
			t.Fatalf("agent still has current task %q", a.CurrentTask)
		}
	}
}

func TestSweepCountsAndNotifies(t *testing.T) {
	b, c := newBoard(t)
	addTask(t, b, "one")
	addTask(t, b, "two")
	b.Claim("AB-1", "alice", time.Minute)
	b.Claim("AB-2", "bob", time.Minute)
	ch, cancel := b.Subscribe()
	defer cancel()
	c.Advance(2 * time.Minute)
	if n := b.Sweep(); n != 2 {
		t.Fatalf("swept %d, want 2", n)
	}
	select {
	case ev := <-ch:
		if ev.Type != "sweep" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no sweep event")
	}
	if n := b.Sweep(); n != 0 {
		t.Fatalf("second sweep released %d", n)
	}
}

func TestHeartbeatExtendsLease(t *testing.T) {
	b, c := newBoard(t)
	addTask(t, b, "job")
	b.Claim("AB-1", "alice", 60*time.Second)
	c.Advance(40 * time.Second)
	if _, err := b.Heartbeat("alice", agentboard.HeartbeatRequest{Kind: "worker", Task: "AB-1"}); err != nil {
		t.Fatal(err)
	}
	c.Advance(40 * time.Second) // 80s after claim: alive only because of the heartbeat
	if d, _ := b.Task("AB-1"); d.Task.Status != agentboard.StatusInProgress {
		t.Fatalf("lease not extended: %+v", d.Task)
	}
	c.Advance(30 * time.Second) // 70s after the heartbeat
	if d, _ := b.Task("AB-1"); d.Task.Status != agentboard.StatusTodo {
		t.Fatalf("lease should have expired: %+v", d.Task)
	}
}

func TestHeartbeat(t *testing.T) {
	tests := []struct {
		name, agent, kind, task string
		wantErr                 error
	}{
		{"registers agent", "carol", "claude-code", "", nil},
		{"own task", "alice", "", "AB-1", nil},
		{"someone else's task", "carol", "", "AB-1", agentboard.ErrNotOwner},
		{"unknown task", "alice", "", "AB-99", agentboard.ErrNotFound},
		{"bad name", "no spaces", "", "", agentboard.ErrInvalid},
		{"long kind", "carol", strings.Repeat("k", 33), "", agentboard.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newBoard(t)
			addTask(t, b, "job")
			b.Claim("AB-1", "alice", 0)
			a, err := b.Heartbeat(tc.agent, agentboard.HeartbeatRequest{Kind: tc.kind, Task: tc.task})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && (!a.Online || a.Name != tc.agent) {
				t.Fatalf("agent = %+v", a)
			}
		})
	}
}

func TestAgentPresence(t *testing.T) {
	b, c := newBoard(t)
	b.Heartbeat("alice", agentboard.HeartbeatRequest{Kind: "worker"})
	if as := b.Agents(); len(as) != 1 || !as[0].Online || as[0].Kind != "worker" {
		t.Fatalf("agents = %+v", as)
	}
	c.Advance(59 * time.Second)
	if !b.Agents()[0].Online {
		t.Fatal("offline too early")
	}
	c.Advance(2 * time.Second)
	if b.Agents()[0].Online {
		t.Fatal("should be offline after the TTL")
	}
}

func TestReleaseAndComplete(t *testing.T) {
	tests := []struct {
		name    string
		op      func(b *agentboard.Board) error
		wantErr error
	}{
		{"release by owner", func(b *agentboard.Board) error { _, err := b.Release("AB-1", "alice"); return err }, nil},
		{"release by other", func(b *agentboard.Board) error { _, err := b.Release("AB-1", "bob"); return err }, agentboard.ErrNotOwner},
		{"release unclaimed", func(b *agentboard.Board) error { _, err := b.Release("AB-2", "alice"); return err }, agentboard.ErrInvalid},
		{"complete by owner", func(b *agentboard.Board) error { _, err := b.Complete("AB-1", "alice"); return err }, nil},
		{"complete by other while leased", func(b *agentboard.Board) error { _, err := b.Complete("AB-1", "bob"); return err }, agentboard.ErrNotOwner},
		{"complete unclaimed", func(b *agentboard.Board) error { _, err := b.Complete("AB-2", "bob"); return err }, nil},
		{"complete twice", func(b *agentboard.Board) error {
			b.Complete("AB-2", "bob")
			_, err := b.Complete("AB-2", "bob")
			return err
		}, agentboard.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newBoard(t)
			addTask(t, b, "one")
			addTask(t, b, "two")
			b.Claim("AB-1", "alice", 0)
			if err := tc.op(b); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
	t.Run("done keeps the assignee", func(t *testing.T) {
		b, _ := newBoard(t)
		addTask(t, b, "one")
		b.Claim("AB-1", "alice", 0)
		got, _ := b.Complete("AB-1", "alice")
		if got.Status != agentboard.StatusDone || got.Assignee != "alice" || got.LeaseExpires != nil {
			t.Fatalf("task = %+v", got)
		}
	})
}

func TestUpdate(t *testing.T) {
	str := func(s string) *string { return &s }
	st := func(s agentboard.Status) *agentboard.Status { return &s }
	pr := func(p agentboard.Priority) *agentboard.Priority { return &p }
	tests := []struct {
		name    string
		patch   agentboard.Patch
		wantErr error
		check   func(*testing.T, agentboard.Task)
	}{
		{"to review keeps assignee, drops lease", agentboard.Patch{Status: st(agentboard.StatusReview)}, nil, func(t *testing.T, k agentboard.Task) {
			if k.Assignee != "alice" || k.LeaseExpires != nil || k.Status != agentboard.StatusReview {
				t.Fatalf("%+v", k)
			}
		}},
		{"to todo clears assignee", agentboard.Patch{Status: st(agentboard.StatusTodo)}, nil, func(t *testing.T, k agentboard.Task) {
			if k.Assignee != "" || k.LeaseExpires != nil {
				t.Fatalf("%+v", k)
			}
		}},
		{"fields", agentboard.Patch{Title: str("New"), Priority: pr(agentboard.PriorityUrgent), Description: str("d")}, nil, func(t *testing.T, k agentboard.Task) {
			if k.Title != "New" || k.Priority != agentboard.PriorityUrgent || k.Description != "d" || k.Status != agentboard.StatusInProgress {
				t.Fatalf("%+v", k)
			}
		}},
		{"bad status", agentboard.Patch{Status: st("nope")}, agentboard.ErrInvalid, nil},
		{"bad priority", agentboard.Patch{Priority: pr("nope")}, agentboard.ErrInvalid, nil},
		{"empty title", agentboard.Patch{Title: str("")}, agentboard.ErrInvalid, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newBoard(t)
			addTask(t, b, "job")
			b.Claim("AB-1", "alice", 0)
			got, err := b.Update("AB-1", tc.patch)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil {
				tc.check(t, got)
			}
		})
	}
	t.Run("unknown task", func(t *testing.T) {
		b, _ := newBoard(t)
		if _, err := b.Update("AB-1", agentboard.Patch{}); !errors.Is(err, agentboard.ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no-op changes nothing", func(t *testing.T) {
		b, _ := newBoard(t)
		addTask(t, b, "job")
		before := len(b.Recent(1000))
		same := agentboard.StatusTodo
		if _, err := b.Update("AB-1", agentboard.Patch{Status: &same, Title: str("job")}); err != nil {
			t.Fatal(err)
		}
		if after := len(b.Recent(1000)); after != before {
			t.Fatalf("activity grew from %d to %d", before, after)
		}
	})
}

func TestActivityIsAppendOnly(t *testing.T) {
	b, c := newBoard(t)
	addTask(t, b, "job")
	b.Claim("AB-1", "alice", 0)
	c.Advance(time.Second)
	b.Comment("AB-1", "alice", "halfway there")
	b.Complete("AB-1", "alice")

	d, err := b.Task("AB-1")
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	var prev int64
	for _, a := range d.Activity {
		if a.ID <= prev {
			t.Fatalf("ids not increasing: %d after %d", a.ID, prev)
		}
		prev = a.ID
		actions = append(actions, a.Action)
	}
	if got := strings.Join(actions, ","); got != "created,claimed,comment,done" {
		t.Fatalf("actions = %s", got)
	}
	recent := b.Recent(2)
	if len(recent) != 2 || recent[0].Action != "done" {
		t.Fatalf("recent = %+v", recent)
	}
}

func TestComment(t *testing.T) {
	b, _ := newBoard(t)
	addTask(t, b, "job")
	for _, tc := range []struct {
		name, id, actor, text string
		wantErr               error
	}{
		{"ok", "AB-1", "alice", "hello", nil},
		{"empty", "AB-1", "alice", "  ", agentboard.ErrInvalid},
		{"too long", "AB-1", "alice", strings.Repeat("x", 5001), agentboard.ErrInvalid},
		{"unknown task", "AB-7", "alice", "hi", agentboard.ErrNotFound},
		{"bad actor", "AB-1", "a b", "hi", agentboard.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := b.Comment(tc.id, tc.actor, tc.text); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestFilterAndSnapshot(t *testing.T) {
	b, _ := newBoard(t)
	b.CreateProject("ZZ", "Other", "")
	addTask(t, b, "a")
	addTask(t, b, "b")
	b.AddTask(agentboard.NewTask{Project: "ZZ", Title: "c"})
	b.Claim("AB-2", "alice", 0)

	if n := len(b.Tasks(agentboard.Filter{Project: "AB"})); n != 2 {
		t.Fatalf("project filter: %d", n)
	}
	if n := len(b.Tasks(agentboard.Filter{Status: agentboard.StatusInProgress})); n != 1 {
		t.Fatalf("status filter: %d", n)
	}
	if n := len(b.Tasks(agentboard.Filter{Assignee: "alice"})); n != 1 {
		t.Fatalf("assignee filter: %d", n)
	}

	s := b.Snapshot()
	if len(s.Projects) != 2 || s.Projects[0].Key != "AB" || len(s.Tasks) != 3 || len(s.Agents) != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Activity[0].ID <= s.Activity[1].ID {
		t.Fatal("activity should be newest first")
	}
}

func TestSnapshotActivityIsCapped(t *testing.T) {
	b, _ := newBoard(t)
	addTask(t, b, "job")
	for i := 0; i < 150; i++ {
		b.Comment("AB-1", "alice", "c")
	}
	if n := len(b.Snapshot().Activity); n != 100 {
		t.Fatalf("snapshot activity = %d, want 100", n)
	}
	if n := len(b.Recent(0)); n != 100 {
		t.Fatalf("recent default = %d, want 100", n)
	}
}

func TestSubscribe(t *testing.T) {
	b, _ := newBoard(t)
	ch, cancel := b.Subscribe()
	addTask(t, b, "job")
	select {
	case ev := <-ch:
		if ev.Type != "task" || ev.TaskID != "AB-1" || ev.At.IsZero() {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no event")
	}
	cancel()
	cancel() // idempotent
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after cancel")
	}
	addTask(t, b, "after cancel") // must not panic or block
}

func TestSlowSubscriberDoesNotBlock(t *testing.T) {
	b, _ := newBoard(t)
	_, cancel := b.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			addTask(t, b, "x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("board blocked on a slow subscriber")
	}
}

func TestConcurrentClaimsHaveOneWinner(t *testing.T) {
	b, _ := newBoard(t)
	addTask(t, b, "contended")
	var wins, conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.Claim("AB-1", fmt.Sprint("agent", i), time.Minute)
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, agentboard.ErrClaimed):
				conflicts.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || conflicts.Load() != 31 {
		t.Fatalf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := t.TempDir() + "/nested/board.json"
	c := &clock{t: time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)}
	open := func() *agentboard.Board {
		b, err := agentboard.Open(agentboard.Options{Store: agentboard.NewFileStore(path), Now: c.Now})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := open()
	b.CreateProject("AB", "Agent Board", "")
	addTask(t, b, "first")
	b.Claim("AB-1", "alice", time.Hour)
	b.Comment("AB-1", "alice", "note")
	if err := b.Close(context.Background()); err != nil { // flush the write-behind queue
		t.Fatal(err)
	}

	b2 := open()
	d, err := b2.Task("AB-1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Task.Assignee != "alice" || len(d.Activity) != 3 {
		t.Fatalf("reloaded = %+v", d)
	}
	// IDs continue where they left off.
	if got, _ := b2.AddTask(agentboard.NewTask{Project: "AB", Title: "second"}); got.ID != "AB-2" {
		t.Fatalf("next id = %s", got.ID)
	}
}
