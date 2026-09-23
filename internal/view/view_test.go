package view_test

import (
	"strings"
	"testing"
	"time"

	"github.com/JiaBao-do/agentboard/internal/view"
	"github.com/JiaBao-do/agentboard/model"
)

var now = time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

func task(id string, kind model.Kind, parent string, st model.Status, p model.Priority, updatedAgo time.Duration) model.Task {
	return model.Task{ID: id, Project: id[:2], Type: kind, Parent: parent, Status: st, Priority: p, UpdatedAt: now.Add(-updatedAgo)}
}

func TestColumnsOrderAndGrouping(t *testing.T) {
	tasks := []model.Task{
		task("AB-1", model.KindTask, "", model.StatusTodo, model.PriorityLow, time.Minute),
		task("AB-2", model.KindTask, "", model.StatusTodo, model.PriorityUrgent, time.Hour),
		task("AB-3", model.KindTask, "", model.StatusTodo, model.PriorityLow, time.Second), // newer than AB-1
		task("AB-4", model.KindTask, "", model.StatusDone, model.PriorityMedium, time.Minute),
		task("ZZ-1", model.KindTask, "", model.StatusReview, model.PriorityMedium, time.Minute),
		{ID: "AB-5", Project: "AB", Status: "bogus"}, // unknown statuses are dropped, not crashed on
	}
	cols := view.Columns(tasks, "")
	if len(cols) != len(model.Statuses) {
		t.Fatalf("columns = %d", len(cols))
	}
	for i, s := range model.Statuses {
		if cols[i].Status != s || cols[i].Label != s.Label() || cols[i].Tasks == nil {
			t.Errorf("column %d = %+v", i, cols[i])
		}
	}
	ids := func(ts []model.Task) (out []string) {
		for _, x := range ts {
			out = append(out, x.ID)
		}
		return
	}
	if got := ids(cols[0].Tasks); len(got) != 3 || got[0] != "AB-2" || got[1] != "AB-3" || got[2] != "AB-1" {
		t.Fatalf("todo order = %v, want urgent first then most recently updated", got)
	}
	if got := ids(view.Columns(tasks, "ZZ")[2].Tasks); len(got) != 1 || got[0] != "ZZ-1" {
		t.Fatalf("project filter = %v", got)
	}
	if n := len(view.Columns(tasks, "NOPE")[0].Tasks); n != 0 {
		t.Fatalf("unknown project matched %d tasks", n)
	}
}

func TestRelTime(t *testing.T) {
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(-3 * time.Second), "just now"},
		{now.Add(-30 * time.Second), "30s ago"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-47 * time.Hour), "47h ago"},
		{now.Add(-72 * time.Hour), "3d ago"},
		{now.Add(90 * time.Second), "in 1m"},
	}
	for _, tc := range tests {
		if got := view.RelTime(now, tc.t); got != tc.want {
			t.Errorf("RelTime(%v) = %q, want %q", now.Sub(tc.t), got, tc.want)
		}
	}
}

func TestLeaseLeft(t *testing.T) {
	exp := func(d time.Duration) *time.Time { e := now.Add(d); return &e }
	tests := []struct {
		name string
		t    model.Task
		want string
	}{
		{"none", model.Task{}, ""},
		{"minutes left", model.Task{LeaseExpires: exp(4*time.Minute + 30*time.Second)}, "4m left"},
		{"seconds left", model.Task{LeaseExpires: exp(20 * time.Second)}, "20s left"},
		{"expired", model.Task{LeaseExpires: exp(-time.Second)}, "lease expired"},
		{"exactly now", model.Task{LeaseExpires: exp(0)}, "lease expired"},
	}
	for _, tc := range tests {
		if got := view.LeaseLeft(now, tc.t); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAgentHelpers(t *testing.T) {
	agents := []model.Agent{{Name: "alice", Online: true}, {Name: "bob"}}
	if !view.AgentOnline(agents, "alice") || view.AgentOnline(agents, "bob") || view.AgentOnline(agents, "ghost") {
		t.Fatal("AgentOnline wrong")
	}
	tasks := []model.Task{{ID: "AB-1", Title: "one"}}
	if view.TaskTitle(tasks, "AB-1") != "one" || view.TaskTitle(tasks, "AB-2") != "" {
		t.Fatal("TaskTitle wrong")
	}
	for in, want := range map[string]string{"claude-code-1": "CC", "alice": "A", "a.b_c": "AB", "": "?", "---": "?", "élan": "É"} {
		if got := view.Initials(in); got != want {
			t.Errorf("Initials(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentKind(t *testing.T) {
	agents := []model.Agent{{Name: "alice", Kind: "claude-code"}, {Name: "bob", Kind: ""}}
	if got := view.AgentKind(agents, "alice"); got != "claude-code" {
		t.Errorf("AgentKind(alice) = %q", got)
	}
	if got := view.AgentKind(agents, "bob"); got != "" {
		t.Errorf("AgentKind(bob) = %q, want empty kind", got)
	}
	if got := view.AgentKind(agents, "ghost"); got != "" {
		t.Errorf("AgentKind(ghost) = %q, want empty for unknown agent", got)
	}
}

func TestAvatarPalette(t *testing.T) {
	// Same kind -> same color, regardless of which agent (name) asks: the
	// function only ever looks at kind, so several different names sharing
	// a kind trivially get the same index; this is the "similar ones reuse
	// the same avatar" behavior the UI relies on.
	kinds := []string{"claude-code", "agent", "builder", "", "script", "human-reviewer"}
	for _, k := range kinds {
		first := view.AvatarPalette(k)
		if first < 0 || first >= view.AvatarPaletteSize {
			t.Fatalf("AvatarPalette(%q) = %d, out of [0,%d)", k, first, view.AvatarPaletteSize)
		}
		for i := 0; i < 5; i++ {
			if got := view.AvatarPalette(k); got != first {
				t.Fatalf("AvatarPalette(%q) not stable/deterministic: got %d then %d", k, first, got)
			}
		}
	}

	// Different kinds -> different colors in most cases. A hash collision
	// across this small a set of real observed kind strings would be rare
	// but is not impossible, so this only spot checks that they don't ALL
	// collide onto one color, never that there are zero collisions.
	seen := map[int]bool{}
	for _, k := range []string{"claude-code", "agent", "builder", "cli-bot", "reviewer"} {
		seen[view.AvatarPalette(k)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("AvatarPalette gave the same color for every real kind string: %v", seen)
	}

	// Edge cases must not panic: empty, unicode, very long, whitespace-only.
	for _, k := range []string{"", "  ", "a", "🤖", strings.Repeat("x", 500)} {
		if got := view.AvatarPalette(k); got < 0 || got >= view.AvatarPaletteSize {
			t.Errorf("AvatarPalette(%q) = %d, out of range", k, got)
		}
	}
}

func TestSummarizeAgents(t *testing.T) {
	tests := []struct {
		name   string
		agents []model.Agent
		want   view.AgentSummary
	}{
		{"empty", nil, view.AgentSummary{}},
		{"all online", []model.Agent{{Name: "a", Online: true}, {Name: "b", Online: true}}, view.AgentSummary{Total: 2, Online: 2, Offline: 0}},
		{"all offline", []model.Agent{{Name: "a"}, {Name: "b"}}, view.AgentSummary{Total: 2, Online: 0, Offline: 2}},
		{"mixed", []model.Agent{{Name: "a", Online: true}, {Name: "b"}, {Name: "c", Online: true}}, view.AgentSummary{Total: 3, Online: 2, Offline: 1}},
	}
	for _, tc := range tests {
		if got := view.SummarizeAgents(tc.agents); got != tc.want {
			t.Errorf("%s: SummarizeAgents = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		a    model.Activity
		want string
	}{
		{model.Activity{Action: "created"}, "created the task"},
		{model.Activity{Action: "claimed", Detail: "lease 10m0s"}, "claimed the task (lease 10m0s)"},
		{model.Activity{Action: "lease_renewed", Detail: "lease 1m0s"}, "renewed the lease (lease 1m0s)"},
		{model.Activity{Action: "released"}, "released the task"},
		{model.Activity{Action: "lease_expired", Detail: "released from bob"}, "lease expired, released from bob"},
		{model.Activity{Action: "status", Detail: "todo → in_progress"}, "moved todo → in progress"},
		{model.Activity{Action: "updated", Detail: "title"}, "changed title"},
		{model.Activity{Action: "done"}, "completed the task"},
		{model.Activity{Action: "comment", Detail: "hi"}, "commented"},
		{model.Activity{Action: "project_created", Detail: "AB n"}, "created project AB n"},
		{model.Activity{Action: "server_stopped"}, "server_stopped"},
		{model.Activity{Action: "custom", Detail: "x"}, "custom x"},
	}
	for _, tc := range tests {
		if got := view.Describe(tc.a); got != tc.want {
			t.Errorf("Describe(%s) = %q, want %q", tc.a.Action, got, tc.want)
		}
	}
}

func TestHierarchyHelpers(t *testing.T) {
	tasks := []model.Task{
		task("AB-1", model.KindEpic, "", model.StatusTodo, model.PriorityMedium, 0),
		task("AB-2", model.KindStory, "AB-1", model.StatusInProgress, model.PriorityMedium, 0),
		task("AB-3", model.KindTask, "AB-2", model.StatusDone, model.PriorityMedium, 0),
		task("AB-4", model.KindTask, "AB-2", model.StatusTodo, model.PriorityMedium, 0),
		task("AB-5", model.KindTask, "AB-1", model.StatusDone, model.PriorityMedium, 0),
		task("AB-6", model.KindStory, "", model.StatusTodo, model.PriorityMedium, 0),     // orphan story
		task("AB-7", model.KindTask, "AB-99", model.StatusTodo, model.PriorityMedium, 0), // dangling parent
	}
	if got := view.EpicOf(tasks, tasks[2]); got != "AB-1" {
		t.Errorf("EpicOf(AB-3) = %q", got)
	}
	if got := view.EpicOf(tasks, tasks[0]); got != "AB-1" {
		t.Errorf("an epic is its own epic, got %q", got)
	}
	if got := view.EpicOf(tasks, tasks[5]); got != "" {
		t.Errorf("orphan story has epic %q", got)
	}
	if got := view.EpicOf(tasks, tasks[6]); got != "" {
		t.Errorf("dangling parent has epic %q", got)
	}
	// A corrupt cycle must terminate.
	cyc := []model.Task{{ID: "AB-1", Parent: "AB-2"}, {ID: "AB-2", Parent: "AB-1"}}
	if got := view.EpicOf(cyc, cyc[0]); got != "" {
		t.Errorf("cycle gave %q", got)
	}

	if got := view.InEpic(tasks, ""); len(got) != len(tasks) {
		t.Errorf("no epic filter should keep all, got %d", len(got))
	}
	in := view.InEpic(tasks, "AB-1")
	if len(in) != 5 { // AB-1..AB-5
		t.Errorf("InEpic(AB-1) = %d tasks", len(in))
	}

	kids := view.Children(tasks, "AB-2")
	if len(kids) != 2 {
		t.Fatalf("children = %d", len(kids))
	}
	if done, total := view.Progress(kids); done != 1 || total != 2 {
		t.Errorf("progress = %d/%d", done, total)
	}
	if done, total := view.Progress(nil); done != 0 || total != 0 {
		t.Errorf("empty progress = %d/%d", done, total)
	}
	if got := view.Epics(tasks); len(got) != 1 || got[0].ID != "AB-1" {
		t.Errorf("epics = %+v", got)
	}
	// Progress of an epic counts every descendant task, not stories.
	if done, total := view.EpicProgress(tasks, "AB-1"); done != 2 || total != 3 {
		t.Errorf("epic progress = %d/%d, want 2/3", done, total)
	}
}

func TestSortAgentsByRecency(t *testing.T) {
	agents := []model.Agent{
		{Name: "stale", LastHeartbeat: now.Add(-2 * time.Hour)},
		{Name: "fresh", LastHeartbeat: now.Add(-time.Minute)},
		{Name: "never"}, // zero LastHeartbeat sorts last
		{Name: "mid", LastHeartbeat: now.Add(-time.Hour)},
	}
	got := view.SortAgentsByRecency(agents)
	want := []string{"fresh", "mid", "stale", "never"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("position %d = %q, want %q (order: %v)", i, got[i].Name, name, names(got))
		}
	}
	// The input slice is untouched.
	if agents[0].Name != "stale" {
		t.Errorf("SortAgentsByRecency mutated its input: %v", agents)
	}
}

func names(agents []model.Agent) []string {
	out := make([]string, len(agents))
	for i, a := range agents {
		out[i] = a.Name
	}
	return out
}

func TestLimit(t *testing.T) {
	s := []int{1, 2, 3, 4, 5}
	if got := view.Limit(s, 3); len(got) != 3 || got[2] != 3 {
		t.Errorf("Limit(5,3) = %v", got)
	}
	if got := view.Limit(s, 10); len(got) != 5 {
		t.Errorf("Limit(5,10) should return all elements, got %v", got)
	}
	if got := view.Limit(s, 0); len(got) != 5 {
		t.Errorf("Limit(5,0) should be unlimited, got %v", got)
	}
	if got := view.Limit([]int(nil), 3); got != nil {
		t.Errorf("Limit(nil,3) = %v, want nil", got)
	}
}

func TestActorState(t *testing.T) {
	agents := []model.Agent{
		{Name: "batchx-builder", Kind: "claude-code", Online: true, CurrentTask: "AB-3"},
		{Name: "idle-bot", Kind: "agent", Online: true},
		{Name: "gone-bot", Kind: "builder"},
	}
	tests := []struct {
		name  string
		agent bool
		text  string
	}{
		{"batchx-builder", true, "working on AB-3"},
		{"idle-bot", true, "online, idle"},
		{"gone-bot", true, "finished"},
		{"user", false, ""},   // a person in the UI is not an agent
		{"system", false, ""}, // the board itself
	}
	for _, tc := range tests {
		s := view.ActorOf(agents, tc.name)
		if s.Agent != tc.agent || view.ActorText(s) != tc.text {
			t.Errorf("%s: %+v %q, want agent=%v %q", tc.name, s, view.ActorText(s), tc.agent, tc.text)
		}
	}
	if s := view.ActorOf(agents, "batchx-builder"); !s.Online || s.Task != "AB-3" || s.Kind != "claude-code" {
		t.Errorf("state = %+v", s)
	}
	if s := view.ActorOf(agents, "user"); s.Kind != "" {
		t.Errorf("non-agent actor got a kind: %+v", s)
	}
}
