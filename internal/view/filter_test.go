package view_test

import (
	"testing"

	"github.com/JiaBao-do/agentboard/internal/view"
	"github.com/JiaBao-do/agentboard/model"
)

func TestFilterMatch(t *testing.T) {
	base := model.Task{
		ID: "AB-1", Project: "AB", Type: model.KindTask, Status: model.StatusTodo,
		Priority: model.PriorityHigh, Assignee: "alice",
		Title: "Fix the Rotating Logs", Description: "logs rotate at midnight",
		Labels: []string{"bug", "Backend"},
	}
	tests := []struct {
		name string
		f    view.Filter
		want bool
	}{
		{"empty filter matches", view.Filter{}, true},
		{"project match", view.Filter{Project: "AB"}, true},
		{"project mismatch", view.Filter{Project: "ZZ"}, false},
		{"status match", view.Filter{Status: model.StatusTodo}, true},
		{"status mismatch", view.Filter{Status: model.StatusDone}, false},
		{"type match", view.Filter{Type: model.KindTask}, true},
		{"type mismatch", view.Filter{Type: model.KindEpic}, false},
		{"priority match", view.Filter{Priority: model.PriorityHigh}, true},
		{"priority mismatch", view.Filter{Priority: model.PriorityLow}, false},
		{"assignee match", view.Filter{Assignee: "alice"}, true},
		{"assignee mismatch", view.Filter{Assignee: "bob"}, false},
		{"unassigned sentinel excludes assigned", view.Filter{Assignee: view.Unassigned}, false},
		{"query matches title case-insensitive", view.Filter{Query: "rotating logs"}, true},
		{"query matches description", view.Filter{Query: "midnight"}, true},
		{"query matches label case-insensitive", view.Filter{Query: "backend"}, true},
		{"query matches id", view.Filter{Query: "ab-1"}, true},
		{"query no match", view.Filter{Query: "nope"}, false},
		{"combined all match", view.Filter{Project: "AB", Status: model.StatusTodo, Priority: model.PriorityHigh, Query: "fix"}, true},
		{"combined one mismatch fails", view.Filter{Project: "AB", Status: model.StatusDone}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.Match(base); got != tc.want {
				t.Errorf("Match(%+v) = %v, want %v", tc.f, got, tc.want)
			}
		})
	}

	if !(view.Filter{Assignee: view.Unassigned}).Match(model.Task{}) {
		t.Error("unassigned sentinel should match a task with no assignee")
	}
}

func TestFilterEmpty(t *testing.T) {
	if !(view.Filter{}).Empty() {
		t.Error("zero value Filter should be Empty")
	}
	if (view.Filter{Query: "x"}).Empty() {
		t.Error("non-zero Filter should not be Empty")
	}
}

func TestFilterTasks(t *testing.T) {
	tasks := []model.Task{
		{ID: "AB-1", Project: "AB", Status: model.StatusTodo, Title: "one"},
		{ID: "AB-2", Project: "AB", Status: model.StatusDone, Title: "two"},
		{ID: "ZZ-1", Project: "ZZ", Status: model.StatusTodo, Title: "three"},
	}
	if got := view.FilterTasks(tasks, view.Filter{}); len(got) != len(tasks) {
		t.Fatalf("empty filter kept %d, want %d", len(got), len(tasks))
	}
	got := view.FilterTasks(tasks, view.Filter{Project: "AB"})
	if len(got) != 2 || got[0].ID != "AB-1" || got[1].ID != "AB-2" {
		t.Fatalf("project filter = %+v", got)
	}
	got = view.FilterTasks(tasks, view.Filter{Status: model.StatusDone})
	if len(got) != 1 || got[0].ID != "AB-2" {
		t.Fatalf("status filter = %+v", got)
	}
}

func TestAssignees(t *testing.T) {
	tasks := []model.Task{
		{ID: "1", Assignee: "bob"},
		{ID: "2", Assignee: "alice"},
		{ID: "3", Assignee: "bob"},
		{ID: "4"},
	}
	got := view.Assignees(tasks)
	want := []string{"alice", "bob"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Assignees = %v, want %v", got, want)
	}
}

func TestFilterQueryEncodeDecode(t *testing.T) {
	if got := view.FilterQuery(view.Filter{}); got != "" {
		t.Errorf("empty filter encoded to %q, want empty", got)
	}
	f := view.Filter{
		Query: "fix logs", Project: "AB", Epic: "AB-1", Status: model.StatusInProgress,
		Assignee: "alice", Type: model.KindTask, Priority: model.PriorityHigh,
	}
	qs := view.FilterQuery(f)
	want := "assignee=alice&epic=AB-1&priority=high&project=AB&q=fix+logs&status=in_progress&type=task"
	if qs != want {
		t.Fatalf("FilterQuery = %q, want %q", qs, want)
	}
	back := view.ParseFilterQuery(qs)
	if back != f {
		t.Fatalf("round trip = %+v, want %+v", back, f)
	}
	// Accepts a leading "?", as location.search provides.
	if back2 := view.ParseFilterQuery("?" + qs); back2 != f {
		t.Fatalf("round trip with leading '?' = %+v, want %+v", back2, f)
	}
}

func TestParseFilterQueryDropsInvalidEnumValues(t *testing.T) {
	f := view.ParseFilterQuery("status=bogus&type=nope&priority=whatever&q=kept&project=AB")
	want := view.Filter{Query: "kept", Project: "AB"}
	if f != want {
		t.Fatalf("ParseFilterQuery with bad enums = %+v, want %+v", f, want)
	}
}

func TestParseFilterQueryMalformed(t *testing.T) {
	// A query string url.ParseQuery cannot parse degrades to an empty filter
	// instead of panicking or propagating an error the caller has nowhere to
	// put (this runs from a JS event handler).
	if got := view.ParseFilterQuery("%zz"); got != (view.Filter{}) {
		t.Fatalf("malformed query = %+v, want zero value", got)
	}
}
