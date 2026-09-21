package view_test

import (
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

func TestActorState(t *testing.T) {
	agents := []model.Agent{
		{Name: "batchx-builder", Online: true, CurrentTask: "AB-3"},
		{Name: "idle-bot", Online: true},
		{Name: "gone-bot"},
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
	if s := view.ActorOf(agents, "batchx-builder"); !s.Online || s.Task != "AB-3" {
		t.Errorf("state = %+v", s)
	}
}

func TestRestartCommand(t *testing.T) {
	if got := view.RestartCommand("127.0.0.1:7878"); got != "agentboard serve -addr 127.0.0.1:7878" {
		t.Fatalf("got %q", got)
	}
}
